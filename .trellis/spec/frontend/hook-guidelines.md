# Hook guidelines

> Custom hooks use React's built-in hooks and are named with the `use` prefix. The project does not use React Query, SWR, Redux hooks, or another hook library for server state.

## Naming and location

- Name hooks `useThing` and place shared hooks in `src/lib/` (`useInViewport.ts`, `useInfiniteListing.ts`, `useListingScrollRestore.ts`, `useDocumentScrollLock.ts`).
- Keep feature hooks beside their feature: `src/shorts/useShortsFeed.ts`, `src/shorts/useShortsKeyboard.tsx`, and `src/shorts/useShortsSwipePager.ts`; admin-specific hooks include `src/admin/useRuntimeLogs.ts` and `src/admin/useLogScroller.ts`.
- Hooks that expose context are also named `use...`: `useAuth` in `src/admin/AuthContext.tsx`, `useToast` in `src/admin/ToastContext.tsx`, `useRouteActivity` in `src/lib/routeActivity.tsx`.
- Hook files can be `.tsx` when the hook accepts/returns `ReactNode` or JSX (`useShortsKeyboard.tsx`, `pageScroll.tsx`); otherwise use `.ts`.

## Custom hook patterns

A hook owns a cohesive stateful behavior and returns a small object, tuple, or value. It must be safe to use with React's rules of hooks: call hooks unconditionally at the top level and put conditional work inside effects/callbacks.

Examples from this codebase:

- `src/lib/useInViewport.ts` returns a boolean and shares one `IntersectionObserver` through a `WeakMap`; it observes on effect setup and unobserves/deletes the callback on cleanup.
- `src/lib/useInfiniteListing.ts` returns `items`, `hasMore`, loading/error flags, a `loadMore` callback, and `retry`. Its reducer owns request transitions while the hook owns `AbortController` cancellation and request identity.
- `src/lib/useListingScrollRestore.ts` coordinates URL/history identity, `sessionStorage`, and scroll restoration. It guards storage access because private browsing/storage failures should only fall back to the top.
- `src/shorts/useShortsSwipePager.ts` keeps gesture and animation decisions in `createShortsSwipePager`; the React hook only connects lifecycle/refs. Follow this split when an event-heavy algorithm needs deterministic unit tests.
- `src/admin/AuthContext.tsx` and `src/admin/ToastContext.tsx` expose context hooks that throw a clear error when used outside their provider. Keep provider placement explicit in `src/main.tsx`.

## Data fetching and effects

- API functions live in `src/data/videos.ts` or `src/admin/api.ts`; a hook invokes them and maps the result to UI state. Do not write endpoint strings in a generic hook or component unless the existing feature already does so.
- For admin data with periodic refresh, use `src/admin/useAdminResource.ts` rather than starting overlapping interval requests in a component. It owns cancellation, stale-data retention, retry, visibility, and online/focus behavior; drive detail uses `useDriveDetailData` and its versioned SSE snapshots. See [Admin Server State](./admin-server-state.md).
- For an effect-triggered request, use an `AbortController` and abort it in the effect cleanup when the API accepts a signal. `useListingQuery` and `useInfiniteListing` are the canonical patterns.
- Guard against stale responses with a request ID/reducer state or an active flag. `useListingQuery` ignores `success`/`failure` actions whose `requestID` is no longer current; `src/pages/SharedVideoPage.tsx` uses an `active` flag for its one-off claim request.
- Separate initial loading, background revalidation, empty data, and errors. `useListingQuery` exposes `initialLoading`, `refreshing`, `transitioning`, and `revalidating`; `useInfiniteListing` retains existing items when loading the next batch.
- Effects that install DOM listeners, timers, observers, media listeners, or locks must remove/clear/release them in the returned cleanup. `src/components/AppShell.tsx` and `src/admin/Modal.tsx` are concrete examples.
- Use `useMemo`/`useCallback` when identity matters to an effect, context value, or child optimization, not as a blanket style rule. `AuthProvider` memoizes its context object and callbacks; `ListingPage` memoizes its feed source from URL inputs.

## State and refs inside hooks

Use state for values that affect rendering; use refs for mutable values that event handlers need without causing a render. `src/components/VideoCard.tsx` keeps preview timers and the video element in refs, while `previewState` and `progress` are state. `ShortsPage` uses refs for active indices, media nodes, and high-frequency playback bookkeeping.

For an external singleton, use `useSyncExternalStore` as in `src/lib/useIsActivePreview.ts` rather than reading a mutable singleton during render. For local persistence, validate and degrade safely as `src/shorts/shortsFeed.ts` does for `localStorage`.

## Common mistakes and current limitations

- Do not create a new observer, interval, event listener, or request on every render. Put setup in an effect with precise dependencies and clean it up.
- Do not omit dependencies just to silence effect behavior. There are a few documented `eslint-disable-next-line react-hooks/exhaustive-deps` comments in `src/components/VideoCard.tsx` and `src/shorts/useShortsSlideGestures.ts`; do not copy them without understanding the ref-based lifecycle they protect.
- Do not call hooks conditionally or from event handlers. Conditional behavior belongs inside the hook's effect/callback.
- There is no shared query cache library. `useListingQuery` has a local bounded LRU cache, infinite feeds and detail prefetching have feature-specific caches in `src/lib`/`src/data`, and each `useAdminResource` call owns a local resource instance (`queryKey` is not a cross-component cache); do not assume React Query invalidation APIs exist.
- The repository has no generated hook types or runtime schema library. Return explicit TypeScript shapes and validate important network/storage boundaries in the API/data layer.
