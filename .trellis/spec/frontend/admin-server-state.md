# Admin server state and drive snapshots

> This spec records the admin API read patterns introduced for drive status and resource synchronization. Source code and tests remain authoritative; keep the frontend and backend snapshot contracts aligned.

## Scenario: Cancellable admin resources and live drive details

### 1. Scope / Trigger

Use this contract when an admin view needs polling server data, preserving the last successful value during refresh, coordinating asynchronous mutations with refreshes, or displaying live per-drive state. The main implementations are `src/admin/useAdminResource.ts`, `src/admin/AdminResource.ts`, `src/admin/RefreshRequest.ts`, `src/admin/drive/driveDetailData.ts`, and `src/admin/api.ts`.

Use `useAdminResource` for independent admin resources such as the drive list, maintenance status, storage summary, and other regularly refreshed data. Use `DriveDetailData` through `useDriveDetailData` for drive detail: it combines independently versioned resources with an SSE stream and polling fallback.

### 2. Signatures

The admin polling hook accepts a loader with an `AbortSignal` and these options:

```ts
type ResourceOptions<T> = {
  queryKey?: string;
  active: boolean;
  intervalMs: number | null | ((data: T) => number | null);
  retryOnError?: boolean;
  maxRetries?: number;
  initialData: T;
  onUnauthorized?: () => void;
};

function useAdminResource<T>(
  load: (signal: AbortSignal) => Promise<T>,
  options: ResourceOptions<T>,
): AdminResourceState<T> & {
  refresh(): Promise<void>;
  invalidate(): Promise<void>;
  setData(value: SetStateAction<T>): void;
};
```

`AdminResourceState<T>` is `{ data, ready, loading, refreshing, error, failure, unauthorized }`. Each `useAdminResource` call owns one resource instance. `queryKey` stabilizes that hook instance when an inline loader changes; it is **not** a global cache key and does not share data across components.

Drive detail uses these resource names and response types:

```ts
type DriveResource = "config" | "runtime" | "stats" | "storage";
type DriveSnapshot<R extends DriveResource = DriveResource> = {
  epoch: string;
  driveId: string;
  resource: R;
  revision: number;
  updatedAt: string;
  data?: DriveResourceData[R];
  error?: string;
  status?: number;
};

getDriveSnapshot<R extends DriveResource>(id: string, resource: R, signal?: AbortSignal)
subscribeDriveSnapshots(
  id: string,
  onSnapshot: (snapshot: DriveSnapshot) => void,
  onOpen: () => void,
  onError: () => void,
)
```

The HTTP read is `GET /admin/api/drives/{id}/{resource}?refresh=true`; the event stream is `GET /admin/api/drives/{id}/events` and emits `snapshot` and `heartbeat` events. `config` contains drive settings, `runtime` contains generation/maintenance state, `stats` contains media counts, and `storage` contains storage usage.

### 3. Contracts

#### Polling resources

- Pass the request's `AbortSignal` to `fetch` and any downstream loader. The owning hook cancels work when it becomes inactive, is paused, or is invalidated; stale completions must not overwrite newer state.
- Keep the last successful `data` while refreshing and after a failed refresh. A failure is not an empty result. Use `ready` to distinguish the initial request from background refresh.
- A timer tick during a healthy in-flight read must not cancel that read. `RefreshRequest` coalesces refresh calls during a flight into one follow-up read; explicit cancellation/invalidation aborts the old flight and ignores its late result.
- The default request timeout is 10 seconds. A timeout aborts the signal and reports a failure so the resource can be retried.
- `active: false` pauses requests and timers. When active, a hidden document pauses the resource; visibility restoration, window focus, or network `online` resumes it with an immediate read.
- `intervalMs` may depend on current data. Errors retry by default with capped exponential backoff (up to 30 seconds); `retryOnError: false` disables retries and `maxRetries` bounds them. Unauthorized errors and non-retryable HTTP 4xx responses are not retried. The resource pauses on unauthorized state and invokes `onUnauthorized` when provided.
- `setData` is a local acknowledgement path for a successful mutation. It cancels older reads before changing the value so a stale response cannot undo the mutation.

#### Versioned drive snapshots

- A snapshot is scoped by `driveId` and `resource`. `epoch` identifies the server-side snapshot service lifetime; a new epoch retires the old one. Compare `revision` only within its resource, not across `config`, `runtime`, `stats`, and `storage`. `updatedAt` is descriptive, not the stale-response ordering key.
- Successful snapshots carry `data`. Error snapshots carry `status` and `error`; the API client recognizes a shaped error snapshot in the HTTP response body except for authorization status 403. Successful JSON is typed at compile time but is not deeply runtime-validated, so keep Go DTOs, TypeScript types, and boundary tests synchronized.
- SSE messages use the same snapshot envelope. A malformed JSON event signals a stream error. The detail controller filters snapshots by drive ID, known resource, positive safe-integer revision, and retired epoch, and ignores older revisions.
- Only a `config` 404 establishes that the drive is missing. A 404 from `runtime`, `stats`, or `storage` triggers a fresh config check instead; it must not immediately delete the displayed drive. A 401/403 stops background synchronization.
- While the detail stream is live, periodic HTTP reads are disabled. On stream failure, the controller closes the old stream, refreshes resources through HTTP, then reconnects with capped backoff. Route/visibility leases pause work; reacquiring a missing drive explicitly resets and revalidates it.
- Applying a newer `runtime` snapshot that moves from busy to idle triggers fresh `stats` and `storage` reads. Mutation responses that include a typed snapshot should be passed to the detail controller's `accept` method rather than waiting for the next poll.

### 4. Validation and error matrix

| Condition | Expected behavior |
| --- | --- |
| Initial loader succeeds | Set `data`, `ready: true`, clear prior failure, and schedule the next interval. |
| Refresh loader fails | Keep the last `data`, expose `error`/`failure`, and retry only when the error is retryable and retry policy allows it. |
| Request times out | Abort the request, report a timeout failure, and release the flight for a later retry. |
| Resource becomes inactive, hidden, or invalidated | Abort/cancel owned work; ignore a late completion from the canceled generation. |
| 401 / `UnauthorizedError` | Mark unauthorized and stop resource polling; invoke `onUnauthorized` when supplied. |
| Non-retryable 4xx | Keep current data and report the error without automatic retry. HTTP 408 and 429 are retryable. |
| Drive snapshot is older in the same resource | Ignore it; do not roll back newer event or mutation data. |
| Drive snapshot changes epoch | Retire the prior epoch and accept current-epoch resource revisions. |
| Non-config drive resource returns 404 | Re-read config to confirm whether the drive was deleted. |
| Config snapshot returns 404 | Mark the drive missing, close the stream, and stop background reads. |
| SSE disconnects or event JSON is malformed | Close the stream, use HTTP fallback, and reconnect with backoff. |

### 5. Good / Base / Bad cases

- **Good:** Independent resources have independent loading/error state; the last usable value remains visible during refresh; cancellation and revision checks prevent stale responses from winning.
- **Base:** A page with one-off data may keep its local request state, provided it forwards `AbortSignal` where available and handles stale responses explicitly.
- **Bad:** Treat a transient error as an empty list, use a single aggregate request that prevents healthy resources from updating, or let polling ticks cancel a slow healthy request.
- **Bad:** Treat `queryKey` as a cross-component cache, or use timestamps/global revisions to compare snapshots from different resources.
- **Bad:** Mark a drive deleted based on a `runtime`, `stats`, or `storage` 404 without confirming the `config` resource.

### 6. Tests required

Use `tests/adminResource.test.ts` for polling-resource changes. Preserve assertions for independent resource completion, stale-data retention, coalesced follow-up reads, abort/late-result behavior, visibility and network resume, retry/backoff, timeout recovery, dynamic intervals, mutation acknowledgements, and query identity.

Use `tests/driveDetailData.test.ts` for drive detail/API changes. Preserve assertions for independent resources, resource-scoped revisions, epoch changes, HTTP/SSE race handling, error snapshots, config-only deletion confirmation, stream reconnection/fallback, authorization shutdown, and task-completion refresh of stats/storage. Add API boundary assertions whenever the Go snapshot envelope or event names change.

### 7. Wrong vs Correct

#### Wrong

```ts
// Overlapping requests can race, failures can erase usable state, and there is
// no request cancellation or stale-response guard.
useEffect(() => {
  const timer = setInterval(async () => setDrives(await api.listDrives()), 5000);
  return () => clearInterval(timer);
}, []);
```

#### Correct

```ts
const drives = useAdminResource(api.listDrives, {
  queryKey: "drives",
  active: routeActive,
  initialData: [],
  intervalMs: (items) => items.some(driveBusy) ? 5000 : 15_000,
});
```

For a mutation that returns an authoritative drive snapshot, apply it through the existing drive-detail controller; do not duplicate the response in separate component state or wait for a polling interval to converge.
