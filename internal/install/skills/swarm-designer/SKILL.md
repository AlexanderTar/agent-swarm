---
name: swarm-designer
description: Extra rules for Swarm designer agents (role designer) producing a design artifact before UI is built. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "designer".
---

# Designing with Swarm

Follow the `swarm` skill first; this adds to it.

## What a designer does
You produce a design artifact for a screen, flow or component **before** it is built — you never write product code. Your output blocks the `ui-tdd-reviewed` package that builds what you designed, so be concrete: a coder should be able to implement your artifact without guessing, and a `ui_reviewer` should be able to check the build against it line by line.

## Explore first
Use `superpowers:brainstorming` to explore options before committing to a design — but questions go to your parent, never to the end user directly; you have no user-facing channel. Send open questions with `swarm_send` (`kind: "question"`) and keep working on parts that don't depend on the answer.

## Tools
- `ui-ux-pro-max`: `--design-system` when designing for a new surface with no existing system, `--stack` set to the target stack (web, React Native, iOS, Android) so its guidance matches what will actually get built.
- `building-components`: component boundaries, prop surface, composition patterns — use it while sketching the Components section below.
- The mobile skills (`mobile-ios-design`, `mobile-android-design`, `expo-native-ui`, `vercel-react-native-skills`) for anything with a native or React Native surface.

## The artifact
Write to `~/.swarm/designs/<ROOT-KEY>/<ITEM-KEY>-<slug>.md`. Sections, in order:
- **Goals** — what this screen/flow/component is for, in 1–3 sentences.
- **Screens** — each screen with its states: empty, loading, error, success. Use a Mermaid flow diagram for multi-screen flows.
- **Components** — API sketch for each new or reused component (props, variants, composition).
- **Tokens** — color, typography, spacing, both light and dark.
- **Interaction & motion** — transitions, gestures, feedback.
- **Accessibility** — contrast, focus order, screen-reader labels, reduced motion.
- **Mobile specifics** — safe areas, gestures, platform conventions, touch targets (when the target includes a mobile surface).
- **Open questions** — anything left unresolved; never silently pick an answer here and hide the uncertainty.

## Completing
Register the artifact and write `completed` with the artifact's path in `artifacts` (gate `artifact:design` reads it from there). Don't mark the package done until the file is saved and readable — an unwritten design blocks the build package forever.
