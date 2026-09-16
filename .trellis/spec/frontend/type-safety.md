# Type safety

> The frontend is TypeScript-first with strict compiler checks. Types are used at React boundaries, API boundaries, reducer actions, and browser/media integrations, but runtime validation is handwritten rather than provided by a schema library.

## Compiler and type organization

`tsconfig.json` enables `strict`, `noUnusedLocals`, `noUnusedParameters`, `noFallthroughCasesInSwitch`, `isolatedModules`, and `noEmit`, with the React JSX transform and `@/*` path alias. The normal type check is `npm run lint` (`tsc --noEmit`). Keep code passing these checks; unused imports/parameters are errors.

- Shared public models live in `src/types.ts`: `VideoItem`, `VideoDetail`, `TagItem`, `SortKey`, `PreviewState`, and collection/comment types are reused by pages, components, data functions, and hooks.
- API-specific types stay close to the API boundary. `src/admin/api.ts` defines admin log, backup, drive, and update response types; `src/data/videos.ts` defines feed cursor/response and share errors.
- Component-only types remain local. Examples include `Props` in `src/components/VideoGrid.tsx`, `ThumbnailState` in `src/components/VideoThumbnail.tsx`, and `AuthStatus`/`AuthCtx` in `src/admin/AuthContext.tsx`.
- Use literal unions for finite state and protocol values (`PreviewState`, `SortKey`, `VideoFeedKind`, `ToastKind`) instead of unconstrained strings. Use discriminated action unions for reducers, as in `ListingQueryAction` in `src/lib/useListingQuery.ts`.
- Use `import type` for type-only dependencies, and prefer generic helpers for typed API results (`apiGet<T>` in `src/data/videos.ts`, `request<T>` in `src/admin/api.ts`).

## API and storage validation

TypeScript types do not validate JSON at runtime. Public data functions therefore check response shapes after `fetch`: `fetchHomeVideos` checks `Array.isArray`, `fetchListing` checks `items` and `total`, and `fetchVideoFeed` checks token length, cursor ordering, total, and exhaustion invariants. Admin `request<T>` parses the response generically, so callers remain responsible for endpoint-specific expectations.

Storage is also treated as untrusted. `src/shorts/shortsFeed.ts` checks parsed bookmark values, token length, integer cursor, and the possibility that `localStorage` is unavailable. Follow that pattern for new local/session storage formats: parse as `unknown`, validate, and return a safe empty/default value on failure.

There is no Zod, Yup, io-ts, generated OpenAPI client, or other runtime schema dependency in `package.json`. Do not document or introduce one as if it already existed.

## Common patterns

```ts
export type PreviewState = "idle" | "intent" | "loading" | "playing" | "error";

export type ListingQueryAction =
  | { type: "disable"; requestID: number }
  | { type: "success"; requestID: number; snapshot: ListingSnapshot }
  | { type: "failure"; requestID: number; error: Error };
```

The codebase uses narrow guards and explicit error conversion at boundaries. `useListingQuery` has `errorValue(error: unknown): Error`, `src/data/videos.ts` uses `instanceof HTTPStatusError` for expected statuses, and `src/admin/api.ts` has typed `UnauthorizedError`/`APIResponseError` classes.

When browser APIs have vendor-specific or library-specific shapes, define a small intersection/interface near the integration instead of leaking an assertion through the component. `src/components/VideoPlayer.tsx` defines types for Artplayer/video/fullscreen integration; `src/shorts/platform.ts` models the optional `navigator.standalone` property.

## Forbidden patterns and current limitations

- Do not introduce `any` for ordinary application data. Narrow `unknown`, add a type guard, or define the missing boundary type. One existing `as any` in `src/components/MainNav.tsx` is a narrow WebKit fullscreen compatibility escape hatch, not a project pattern to copy.
- Avoid unchecked assertions (`as SomeType`) when a guard can establish the shape. The current code necessarily has assertions around DOM event targets, third-party Artplayer internals, YAML nodes, and vendor fullscreen APIs; keep such assertions local and explain the integration constraint.
- Do not silence strict errors with `// @ts-ignore`, broad `as unknown as`, or non-null assertions unless the browser/library contract makes the alternative impossible and the reason is documented. Existing tests use a few casts to construct partial fixtures, but production code should model the real shape.
- Do not use optional properties to hide a required state transition. Use a union when states have different valid fields; `PreviewState`, feed response types, and reducer actions demonstrate this.
- Generic API functions are not runtime validators. Never assume `request<T>` or `apiGet<T>` makes server JSON trustworthy; validate response fields in the data/API function.
- This repository does not generate frontend types from backend Go structs. When an API contract changes, update the TypeScript model and its boundary validation together.
