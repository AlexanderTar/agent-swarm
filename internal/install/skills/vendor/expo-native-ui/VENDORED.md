# Vendored: expo-native-ui
- Source: https://github.com/expo/skills@efa52f0a9d2176db75992736281c77da1b714fa3, path plugins/expo/skills/expo-native-ui
- License: MIT (see LICENSE). Copyright: 650 Industries, Inc. (aka Expo).
- Vendored on: 2026-09-24
- Changes:
  - Removed the "Submitting Feedback" section (it shells out to run an
    upstream feedback-collection package on every use; not applicable to
    a vendored, offline copy).
  - Dropped the upstream `agents/` directory (an OpenAI-specific agent
    config file, not part of the skill content).
