---
name: backlite-read
description: Stub. Slice 6 (#50) will populate this skill with read-mode instructions and migrate the read-* helper scripts.
---

# Backlite read skill (stub)

This skill is a placeholder. It exists so the skill-agent image can build
with the directory layout the entrypoint expects. The real instructions, the
status payload schema for read-mode, and the helper scripts (read-embed.sh,
read-similar.sh, read-lookup.sh) land in Slice 6 (issue #50).

For now, an agent dispatched here should write a failure `status.json` with
`error: "read skill not yet implemented (issue #50)"` and exit non-zero.
