# Frontend development guidelines

> These guides describe the current Vite + React frontend in this repository. They are codebase-backed conventions, not a replacement for the task requirements or the TypeScript/build configuration.

## Guidelines index

| Guide | Description |
|-------|-------------|
| [Directory Structure](./directory-structure.md) | Route, component, admin, data, hook, and style organization |
| [Component Guidelines](./component-guidelines.md) | Function components, props, global CSS, composition, accessibility |
| [Hook Guidelines](./hook-guidelines.md) | Custom hooks, effects, cancellation, refs, and data fetching |
| [State Management](./state-management.md) | Local state, URL state, Context, browser persistence, and server state |
| [Admin Server State](./admin-server-state.md) | Admin polling resources, versioned drive snapshots, and SSE synchronization |
| [Type Safety](./type-safety.md) | Strict TypeScript, shared models, unions, boundary validation |
| [Quality Guidelines](./quality-guidelines.md) | Type checks, Node tests, builds, accessibility, and review checks |

## How to use these guides

Read the guide for the layer being changed and inspect the referenced example before adding a new pattern. Existing code is the source of truth: where a feature has a deliberate exception (for example, vendor media APIs or source-contract tests), the relevant guide records that limitation instead of presenting it as a universal rule.

Documentation is written in English. Source comments and user-facing strings currently include both English and Chinese; preserve the surrounding file's language and behavior when editing source.
