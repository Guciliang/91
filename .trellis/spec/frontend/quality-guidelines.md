# Quality guidelines

> Quality is enforced primarily by TypeScript, focused Node tests, Vite builds, and source/CSS contract tests. There is no ESLint configuration, browser component test runner, or automated accessibility scanner in the current frontend toolchain.

## Required checks

From the repository root:

```bash
npm run lint      # tsc --noEmit
npm test          # node --import tsx --test tests/*.test.ts
npm run build     # tsc -b && vite build
npm run check     # npm audit && lint && test
npm run verify    # check followed by build
```

`package.json` requires Node `>=22.12.0`. Run the focused test file(s) relevant to a change while iterating, then run at least `npm run lint` and the relevant test set. Do not claim a browser interaction was tested when only a source contract test ran.

## Testing patterns

Tests use the Node built-in `node:test` runner and `node:assert/strict`, with TypeScript loaded by `tsx`. Many tests exercise pure logic directly:

- `tests/listingQueryState.test.ts` tests reducer/display transitions, stale response rejection, and cache behavior from `src/lib/useListingQuery.ts`.
- `tests/shortsFeedLogic.test.ts` tests queue merging, validated `localStorage` bookmark handling, prefetch decisions, and trimming from `src/shorts/shortsFeed.ts`.
- `tests/tripleScreen.test.ts`, `tests/playerGestures.test.ts`, and `tests/virtualGrid.test.ts` cover browser/media algorithms without mounting the full application.

Some tests intentionally read source and CSS as text to protect implementation contracts. `tests/videoGridSkeleton.test.ts` checks the skeleton markup and CSS, `tests/adminModalFocus.test.ts` checks modal focus/backdrop invariants, and `tests/homeFeedTabs.test.ts` checks URL/tab/accessibility markup. Preserve those contracts or update tests deliberately when the behavior really changes.

There is no React Testing Library/jsdom setup in `package.json`; do not write tests that assume a browser DOM test harness is installed. Extract pure functions or test source/CSS contracts using the established Node patterns instead.

## Accessibility requirements

Accessibility is part of component correctness:

- Use semantic landmarks/headings and native controls. Use `type="button"` for non-submit buttons, real `Link`/anchors for navigation, and form labels/`aria-label` for icon-only controls.
- Keep keyboard focus visible. `src/styles/base.css` defines `:focus-visible`; do not globally remove it.
- Mark decorative icons, images, progress bars, starfield, and skeletons with `aria-hidden="true"`; give the containing loading/error region a meaningful `role="status"` or `role="alert"` and label.
- Use `aria-pressed` for toggles (`src/components/SortToolbar.tsx`, `src/components/VideoActions.tsx`), `aria-selected`/`aria-controls` for tabs (`src/components/HomeFeedTabs.tsx`, `src/components/RecommendedRail.tsx`), and `aria-current` for the active collection item.
- Use the established dialog implementation in `src/admin/Modal.tsx` for admin dialogs. It provides a portal, `role="dialog"`, `aria-modal`, title/label, Escape handling, focus trapping, and focus restoration.
- Respect reduced motion. Global CSS in `src/styles/base.css` reduces animation and transition duration under `prefers-reduced-motion`; new motion should not bypass that behavior.

## Performance and reliability patterns

- Cancel fetches and remove listeners/timers/observers on cleanup. Keep existing content visible during background revalidation where the feature supports it.
- Use stable keys and avoid unnecessary high-frequency renders. `VirtualVideoGrid` uses `@tanstack/react-virtual`; `VideoCard` is memoized; Shorts progress updates use refs/CSS variables where possible.
- Preserve feed consistency with the server snapshot cursor/token instead of independently appending random pages. `src/lib/infiniteFeedSource.ts` and `src/lib/useInfiniteListing.ts` own this contract.
- Keep user-facing empty, loading, unavailable, and error states distinct. `AuthUnavailable`, `ListingLoadError`, `InfiniteFeedStatus`, `VideoGrid`, and `VideoDetailLoading` provide existing patterns.

## Forbidden patterns and common failures

- Do not add a dependency or toolchain (ESLint, test runner, CSS framework, query library) merely because it is common elsewhere; verify `package.json` and follow the existing scripts first.
- Do not use `console` logging as an error-handling strategy or turn transport failures into empty content. Return typed errors and render the existing error/retry state.
- Do not bypass `npm run lint` with unused variables, ignored strict errors, or disabled checks. A narrowly scoped existing lint suppression in `src/shorts/useShortsSlideGestures.ts` documents a ref-based effect dependency; new suppressions need the same kind of reason.
- Do not remove ARIA labels, focus handling, or reduced-motion behavior to simplify markup. The source-contract tests specifically protect modal, tab, loading, and player behavior.
- Do not alter unrelated staged application files in a worktree with an in-progress merge. Frontend guideline changes belong in `.trellis/spec/frontend/`; keep implementation changes scoped and reviewable.

## Review checklist

Before considering a frontend change complete, verify:

1. The component/hook lives in the established directory and uses the existing alias/export/style pattern.
2. Props, reducer actions, API responses, and storage values have explicit types and boundary validation.
3. Async work can be cancelled or ignores stale responses, and all installed resources are cleaned up.
4. Loading, empty, error, retry, and background-refresh states remain distinguishable.
5. Keyboard/focus behavior, semantic controls, ARIA state, decorative content, and reduced motion are covered.
6. Focused tests reflect the changed pure logic or source/CSS contract.
7. `npm run lint`, relevant `npm test` files, and (for build-affecting changes) `npm run build` pass.
