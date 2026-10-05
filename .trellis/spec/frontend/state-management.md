# State management

> State is managed with React 18 primitives and small feature-local services. There is no Redux, Zustand, MobX, or TanStack Query store in the frontend dependencies.

## State categories

### Component-local UI state

Use `useState` for state that belongs to one mounted component: dialogs, form drafts, loading flags, selected tabs, media controls, and transient animation state. Examples include the `open`, `title`, and `tags` state in `src/pages/UploadPage.tsx`, preview state in `src/components/VideoCard.tsx`, and modal/focus state in `src/admin/Modal.tsx`.

Use `useReducer` when transitions or request identity are complex. `src/lib/useListingQuery.ts` models `idle`, `initial-loading`, `refreshing`, `ready`, and `error`; `src/lib/useInfiniteListing.ts` manages append/retry/expired-feed transitions.

### URL and navigation state

Search/filter/sort/view state belongs in React Router search params so reloads, links, and history can reproduce it. `src/pages/HomePage.tsx` uses `useSearchParams` for the home feed, and `src/pages/ListingPage.tsx` uses it for `q`, `tag`, `sort`, and `view`. Helpers in `src/lib/listingSearchParams.ts` normalize and update those values. Route transitions use `Link`, `Navigate`, and router state rather than a separate global navigation store.

### Context state

Use Context only for genuinely cross-cutting session/UI concerns:

- `AuthProvider` in `src/admin/AuthContext.tsx` owns authentication status, role, login/logout, refresh, and session invalidation; `useAuth` is consumed by `RequireAuth`, `RequireAdmin`, and pages.
- `ToastProvider` in `src/admin/ToastContext.tsx` owns the bounded visible toast list and timers; `useToast` exposes `show`.
- `PageScrollRootProvider` in `src/lib/pageScroll.tsx` and `RouteActivityProvider` in `src/lib/routeActivity.tsx` provide narrowly scoped browser/layout coordination.

Do not put ordinary page data or one page's form draft into a global context.

### External and persisted state

Small browser services own state whose lifetime is not a component:

- `src/lib/previewController.ts` is a singleton external store observed by `useIsActivePreview` so only one card preview is active.
- `src/lib/theme.ts` synchronizes the `data-theme` attribute and `localStorage`; startup also reads the value in `index.html` before React mounts.
- `src/shorts/shortsFeed.ts` stores a validated Shorts bookmark in `localStorage`.
- `src/lib/useListingScrollRestore.ts` and `src/lib/videoReturnPath.ts` use `sessionStorage` for navigation-scoped restoration.

Storage is an enhancement, not the source of truth for server authorization or video data; failure to access storage must degrade to a safe default.

## Server state

Server state is fetched through typed functions in `src/data/videos.ts` and `src/admin/api.ts`. Public video-data functions validate important response invariants (for example, `fetchListing` checks `items` and `total`, while `fetchVideoFeed` validates token/cursor relationships); the generic admin `request<T>` helper is not a runtime validator, so feature boundaries must validate any required shape.

Use a hook appropriate to the data lifetime:

- `src/lib/useListingQuery.ts` is a bounded, keyed snapshot cache for page-style listing queries.
- `src/lib/useInfiniteListing.ts` with `src/lib/infiniteFeedSource.ts` owns append-only feed snapshots, cursors, cancellation, retry, and restoration.
- `src/pages/VideoDetailPage.tsx` maintains detail/recommendation/tag request state and uses the prefetch helpers in `src/data/videos.ts`.
- `src/lib/useLazyVideoCollection.ts` fetches a collection only when the UI opens/needs it.
- Admin resources that need periodic refresh use `src/admin/useAdminResource.ts`; drive detail uses the per-drive snapshot controller and SSE/polling fallback in `src/admin/drive/driveDetailData.ts`. See [Admin Server State](./admin-server-state.md) for their request, revision, and error contracts.

Keep a committed snapshot visible while a new query is pending when the feature supports it. `deriveListingQueryDisplay` distinguishes a blocking query transition from background revalidation, and `VideoGrid`/`VirtualVideoGrid` render those modes separately.

## Derived state and updates

Prefer deriving booleans and display modes from canonical state rather than storing duplicate flags. `ListingPage` derives `hasContent`, `showSkeleton`, `showEmptyError`, and `showTailError` from the listing result. Use functional state updates for values that depend on the previous state, as in `ToastContext` and Shorts queue management.

When a server mutation is optimistic, keep rollback/error handling explicit. `src/components/VideoActions.tsx` owns reaction pending state and reports resulting counts; `src/pages/VideoDetailPage.tsx` keeps a reaction-count ref so a background detail refresh does not overwrite a recent local reaction.

## Anti-patterns and limitations

- Do not add a global store for convenience. First ask whether the value belongs in component state, URL params, a provider already present, or a feature service.
- Do not use URL params for ephemeral animation or modal state unless the route contract requires it; ordinary page-local state is the current pattern.
- Do not mutate React state, feed arrays, or cached snapshots in place. `src/shorts/shortsFeed.ts` explicitly returns a new queue and tests that the previous queue is unchanged.
- Do not treat a network failure as an empty result. `useShortsFeed` keeps `loadError` separate from `empty`, and listing components distinguish empty from error-with-content.
- There is no app-wide query cache or cross-feature invalidation mechanism. `useAdminResource` exposes `refresh`/`invalidate` for its own instance, while drive detail has a separate per-drive snapshot controller. Invalidate through the owning hook/cache helper and document any additional feature-local cache boundary.
