# Frontend directory structure

> The frontend is a Vite + React 18 single-page application. The structure is organized by route, reusable UI, and domain utilities rather than by a separate package for each feature.

## Directory layout

```text
src/
├── main.tsx                 # React root, providers, router, global CSS
├── App.tsx                  # route tree, lazy route modules, route-level effects
├── types.ts                 # shared public-domain types and discriminated unions
├── pages/                   # route-level screens
│   ├── HomePage.tsx
│   ├── ListingPage.tsx
│   ├── VideoDetailPage.tsx
│   ├── ShortsPage.tsx
│   ├── SharedVideoPage.tsx
│   └── UploadPage.tsx
├── components/              # reusable public UI and feature subcomponents
│   ├── AppShell.tsx
│   ├── VideoCard.tsx
│   ├── VideoPlayer.tsx
│   ├── VirtualVideoGrid.tsx
│   └── icons/
├── admin/                   # authenticated admin UI, API, contexts, and subfeatures
│   ├── AdminLayout.tsx
│   ├── api.ts
│   ├── drive/
│   ├── settings/
│   └── icons/
├── shorts/                  # Shorts-specific state machine/helpers and hooks
├── data/                    # backend-facing public data functions and static data
│   └── videos.ts
├── lib/                     # shared hooks, browser services, caches, and pure domain logic
└── styles/                  # global CSS, design tokens, and feature styles

public/                      # files copied as-is by Vite
src/assets/                  # imported images bundled by Vite
```

The exact route wiring is in `src/App.tsx`; the application is mounted in `src/main.tsx` with `React.StrictMode`, `BrowserRouter`, `ToastProvider`, and `AuthProvider`. Public route pages are lazy-loaded, and admin pages are split through `src/admin/adminPageModules.ts`.

## Module organization

- Put a route screen in `src/pages/` and keep route-specific orchestration there. For example, `src/pages/ListingPage.tsx` reads URL filters, chooses `listingFeedSource`, invokes `useInfiniteListing`, and composes `VirtualVideoGrid`; it does not implement the feed reducer itself.
- Put reusable public UI in `src/components/`. `src/components/VideoGrid.tsx` and `src/components/VideoCard.tsx` are examples of components that can be composed by multiple pages.
- Keep admin-only code under `src/admin/`. Admin drive forms live in `src/admin/drive/`, settings editor code in `src/admin/settings/`, and shared admin behavior such as authentication and toasts at the `src/admin/` root.
- Keep Shorts-specific non-UI algorithms beside the Shorts UI in `src/shorts/`. `src/shorts/shortsFeed.ts` and `src/shorts/slideVisibility.ts` are pure/testable logic; `src/shorts/useShortsFeed.ts` and `src/shorts/useShortsSwipePager.ts` adapt that logic to React.
- Put cross-feature browser behavior and shared hooks in `src/lib/`. Examples include `src/lib/useInViewport.ts`, `src/lib/useListingScrollRestore.ts`, `src/lib/pageScroll.tsx`, and `src/lib/previewController.ts`.
- Put public API functions in `src/data/videos.ts`; put the admin API client and admin response types in `src/admin/api.ts`. Components should call these modules or a hook rather than embedding endpoint construction in JSX.
- Keep shared domain models in `src/types.ts` when they cross pages/components. Small types used only by one component or hook remain local, as in `Props` in `src/components/AppShell.tsx` and `ThumbnailState` in `src/components/VideoThumbnail.tsx`.

## Naming and imports

- Use PascalCase filenames for React components (`VideoCard.tsx`, `AdminLayout.tsx`, `ShortsPage.tsx`) and camelCase filenames for utilities/hooks (`useInfiniteListing.ts`, `listingSearchParams.ts`, `videoReturnPath.ts`). CSS files are kebab-case (`video-card.css`, `admin-controls.css`).
- Export components with named exports in shared modules (`export function AppShell`, `export const VideoCard`) and use a default export for route pages (`export default function ListingPage`). Follow the existing file when editing it.
- Use the `@/*` alias for imports from `src` (`import { VideoGrid } from "@/components/VideoGrid"`). Relative imports are used within a local feature, such as `./mediaBuffer` from `src/shorts/useShortsSwipePager.ts` and `./api` from admin code.
- Import types with `import type` where practical. `tsconfig.json` enables strict checking, `noUnusedLocals`, `noUnusedParameters`, and `noFallthroughCasesInSwitch`.

## Examples to copy

- Public route composition: `src/pages/ListingPage.tsx`.
- Public shared shell and lifecycle behavior: `src/components/AppShell.tsx`.
- Admin feature boundary: `src/admin/drive/DriveForm.tsx` and `src/admin/settings/ConfigSourceWorkspace.tsx`.
- Pure logic separated from React: `src/lib/useListingQuery.ts` (reducer/cache) and `src/shorts/shortsFeed.ts` (feed rules).

## Current limitations and anti-patterns

- There is no feature-folder convention below `src/pages/` for ordinary public pages; do not invent a parallel `features/` tree without changing the established structure.
- CSS is global, not CSS Modules, Tailwind, or styled-components. A component's class names are styled in `src/styles/*.css`, so do not add a local CSS module as if one were configured.
- `src/types.ts` is a broad shared model file and `src/admin/api.ts` contains many admin API types. This is current organization, not a claim that every type belongs in one global file.
- Do not edit `README.md` as part of frontend implementation. The repository also contains backend code and vendored dependencies; frontend guidance applies to `src/`, `tests/`, and the root Vite/TypeScript configuration.
