---
name: web-design-guidelines
description: Review UI code for Web Interface Guidelines compliance. Use when asked to "review my UI", "check accessibility", "audit design", "review UX", or "check my site against best practices".
metadata:
  author: vercel
  version: "1.0.0"
  argument-hint: <file-or-pattern>
---

# Web Interface Guidelines

Review files for compliance with Web Interface Guidelines.

## How It Works

1. Read the vendored guidelines at `references/rules.md`
2. Read the specified files (or prompt user for files/pattern)
3. Check against all rules in `references/rules.md`
4. Output findings in the terse `file:line` format

## Guidelines Source

The rules live at `references/rules.md`, vendored from vercel-labs's Web
Interface Guidelines (see `VENDORED.md`). Read that file directly instead of
fetching it from the network — it ships with this skill.

## Usage

When a user provides a file or pattern argument:
1. Read `references/rules.md`
2. Read the specified files
3. Apply all rules from `references/rules.md`
4. Output findings using the format specified in `references/rules.md`

If no files specified, ask the user which files to review.
