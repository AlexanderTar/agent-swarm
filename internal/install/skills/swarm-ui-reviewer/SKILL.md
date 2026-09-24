---
name: swarm-ui-reviewer
description: Extra rules for Swarm ui_reviewer agents (role ui_reviewer) reviewing UI work. Use together with the `swarm` skill when your kickoff names this skill or your assignment has a `## Workflow` step naming you "ui_reviewer".
---

# Reviewing UI with Swarm

Follow the `swarm` skill first; this adds to it. Then read the `swarm-reviewer` skill in full — it is not in your kickoff, but everything in it applies to you: review order (spec/acceptance → tests → correctness → security → simplicity/`ponytail-review` → conventions), walking the package unit by unit, never editing files, and the verdict/findings contract below. This skill only adds the UI-specific checks and sources.

## Verdict and findings, restated
`completed` carries `verdict` (`pass` | `changes_requested` | `blocked`) and `findings[]` (`{severity: critical|major|minor|nit, file, line, summary, unit}`), same as `swarm-reviewer`. `pass` is allowed only when every remaining finding is `nit`/`minor`; a missing unit is always at least `major`. Never edit files.

## UI-specific checks, after the standard review order
- **Visual and interaction quality**: run `web-design-guidelines` against every changed UI file (web) — layout, spacing, typography, color usage, states (empty/loading/error/success), responsiveness.
- **Component API quality**: check new or changed components against `building-components` — prop surface, composition over boolean-prop proliferation, sensible defaults, reusability.
- **Design artifact honoured**: if the task (or one it depends on) has a `design` artifact registered, read it (`swarm_read`) and confirm the built UI matches it — screens, states, tokens, interaction. A deviation that isn't called out in the coder's `completed` summary is a finding.
- **Mobile**: for React Native / Expo or native iOS/Android surfaces, apply whichever of `mobile-ios-design`, `mobile-android-design`, `expo-native-ui`, `vercel-react-native-skills` fits the platform, plus the `ui-ux-pro-max` pre-delivery checklist (`references/pro-rules.md`): safe areas, touch targets ≥ 44pt (iOS) / 48dp (Android), dynamic type / font scaling, reduced motion, dark mode, contrast.

Cite the rule source in every UI finding (e.g. "web-design-guidelines: insufficient contrast" or "ui-ux-pro-max pro-rules.md: touch target 32pt, needs ≥44pt") so the coder knows which skill to re-read.
