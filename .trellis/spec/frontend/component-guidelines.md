# Component guidelines

> Components are ordinary React function components using TypeScript and JSX. They compose semantic HTML, global CSS classes, and small local state; there is no component library or CSS-in-JS layer in this repository.

## Component structure

Use a local `Props` type near the component, destructure props in the function signature, and keep the render branches readable. `src/components/AppShell.tsx` is the small version of this pattern:

```tsx
type Props = {
  children: ReactNode;
  mobileAutoHideNav?: boolean;
};

export function AppShell({ children, mobileAutoHideNav = false }: Props) {
  // hooks and event/lifecycle logic
  return (
    <div className="app-shell">
      <main className="app-shell__main">{children}</main>
    </div>
  );
}
```

For a data-bearing component, use explicit domain types and defaults. `src/components/VideoCard.tsx` accepts `video: VideoItem`, optional loading priority flags, and is wrapped in `memo` because it appears in large grids. `src/components/VideoGrid.tsx` owns grid-level loading/empty/refresh branches and delegates each item to `VideoCard`.

Route pages compose components rather than becoming generic UI primitives. `src/pages/ListingPage.tsx` composes `SearchPanel`, `TagCloud`, `SortToolbar`, `VirtualVideoGrid`, and `ListingLoadError`; `src/pages/VideoDetailPage.tsx` composes the player, metadata, actions, and recommendation rail.

## Props and composition

- Prefer a local object `Props` type for component-only props. Reuse `VideoItem`, `VideoDetail`, `TagItem`, and other models from `src/types.ts` when the value crosses module boundaries.
- Use `ReactNode` for slots and children. `AppShell` accepts `children`; `src/admin/Modal.tsx` accepts `children` and an optional `footer` slot. Do not pass arbitrary HTML through a string when a typed node slot is enough.
- Callbacks are named `on...` (`onClose`, `onRetry`, `onReactionCountsChange`, `onVisibilityChange`) and are typed with their actual event/value arguments. Boolean options are named positively (`eager`, `highPriority`, `restoreFocus`) and receive defaults during destructuring.
- Keep route state and fetching orchestration in the page or a hook. Components such as `VideoCard` can own interaction state for their own preview, but should receive the domain item and report durable changes through callbacks/API modules.
- Use `key` values based on stable domain identity (`key={v.id}` in `VideoGrid`). Do not use array indexes for real video rows; indexes are only used for intentionally anonymous skeleton placeholders.
- Use named exports for shared components and default exports for route pages, matching the existing module. Avoid adding a second abstraction when a nearby component already owns the behavior.

## Styling patterns

Styles are global CSS imported from `src/main.tsx`, an admin entry/layout module, or a feature route when the styles are feature-specific (for example, `src/pages/ShortsPage.tsx`). Class names use component/element/state conventions such as `video-card__link`, `video-grid-loading`, `is-compact`, and `is-busy`. Design values come from `src/styles/tokens.css` (`var(--space-4)`, `var(--accent)`, `var(--radius-md)`) and should not be duplicated in JSX or a new styling system.

Examples:

- `src/components/VideoCard.tsx` uses `video-card`, `thumb-frame`, and `video-title`; its styles are in `src/styles/video-card.css`.
- `src/components/VideoGrid.tsx` uses `is-compact`, `is-busy`, and `video-grid-background-status` to represent state without changing the component boundary.
- `src/admin/AdminLayout.tsx` imports `admin-controls.css` and `admin.css` because the admin layout owns the admin style boundary.

Inline style is present for small dynamic values (for example the preview progress width in `VideoCard` and spinner CSS variables in `ShortsPage`), but static styling belongs in CSS.

## Accessibility patterns

- Prefer native elements: use `<button type="button">` for actions, `<a>`/React Router `<Link>` for navigation, and real headings/landmarks. `src/components/VideoCard.tsx` uses a `Link` around the card; `src/components/SearchPanel.tsx` uses `role="search"` and a submit button.
- Give icon-only controls an `aria-label` and mark decorative Lucide icons/images `aria-hidden="true"`. See `src/components/BackToTop.tsx`, `src/components/VideoActions.tsx`, and `src/admin/Modal.tsx`.
- Express state with the appropriate ARIA contract: `aria-pressed` for toggles (`VideoActions`, `SortToolbar`), `aria-selected`/`aria-controls` for tabs (`HomeFeedTabs`, `RecommendedRail`), `aria-busy` and `role="status"` for loading regions (`VideoGrid`), and `role="alert"` for actionable errors.
- Modal code must preserve the established focus behavior. `src/admin/Modal.tsx` portals to `document.body`, labels the dialog with `useId`, focuses the first focusable element, traps Tab, handles Escape, and restores opener focus. Reuse it instead of making a second ad hoc modal.
- Preserve the global `:focus-visible` rules in `src/styles/base.css`; do not remove outlines merely to match a visual design. Honor `prefers-reduced-motion`, already handled globally in that file.

## Common mistakes to avoid

- Do not make a clickable `<div>` or `<span>` where a button/link is appropriate. If a complex media surface needs custom interaction, follow the existing keyboard and ARIA handling in `src/components/VideoPlayer.tsx`.
- Do not put a decorative icon or skeleton into the accessibility tree. Existing loading placeholders in `VideoGrid` use `aria-hidden`, while the surrounding region provides `role="status"`.
- Do not close `Modal` on backdrop clicks: the current modal intentionally does not treat the backdrop as a close control, and its tests enforce that behavior.
- Do not introduce a second modal, toast, or global shell implementation. Use `Modal`, `useToast`, and `AppShell`.
- Do not make a component re-render the entire feed for high-frequency media progress when a ref/CSS update is enough. `src/pages/ShortsPage.tsx` deliberately keeps progress refs and writes CSS variables to avoid rebuilding the whole slide every `timeupdate`.
