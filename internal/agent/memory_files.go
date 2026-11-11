package agent

const MemoryFilesRule = `

Agent memory files (AGENTS.md and CLAUDE.md) - limits on your own changes:
- This rule governs edits you would initiate on your own as pipeline work. Do not independently create, modify, rename, or delete these files, and do not add or rewrite their content just because something seems missing, stale, or wrong.
- If these files are part of the change under validation, review their changes like any other file. Never flag them merely for being changed; you may report inaccurate content as you would in any other file.
- In a fix turn, when a finding or recorded human fix decision concerns memory-file content that is part of the change under validation, you may edit those files to address it. Do not make unrelated or otherwise unprompted memory-file edits.
- They load into every future agent session, so additions or rewrites outside those prompted changes remain a deliberate human decision, not automated pipeline output.`

const MemoryFilesConflictRule = `- For AGENTS.md and CLAUDE.md, make no independent content edits beyond resolving the conflict itself: resolve their conflicts yourself whether they have conflict markers or are modify/delete or add/add conflicts. For modify/delete, decide whether to keep or remove the file based on the two sides; stage the resolution. Make no other edits to their content - never add, rewrite, or restructure it outside the conflict.`
