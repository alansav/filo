# Filo: history

**The past.** Of three documents, this is the past: `history.md` (how Filo got here), `design.md` (the present:
what it is) and `roadmap.md` (the future: what it is not yet).

How Filo was built: the build log, decisions that were replaced, and the notes that explain how the design got to
where it is. Nothing here describes Filo as it is; that is `design.md`. Work not done yet is in `roadmap.md`.

## Status, as of 2026-11-17

Iteration 1 (scan, register, report) is done: it passes its test-tree criterion, and on 2026-11-11 a real home
directory (5,582 files, about 3.7 GB) scanned completely and rescanned quickly, reporting only real changes. Iteration
2 was reshaped on 2026-11-12 from a review queue into one keep-until date per file (the retention CSV, `filo export
retention` and `filo update`), with root settings stored on the root; it passes its test-tree criterion, and setting
dates on the real home directory is still to be done. A C# prototype exists as a reference spec only; its schema and
reconcile rules were carried over. The second shape of the retention CSV (`file_date`, `retention`, schema version
5) is implemented. Iteration 3 (deletion) was designed on 2026-11-12 and implemented on 2026-11-13 (schema version
6); it passes its test-tree criterion and has not yet been run on real files. Register snapshots (rolling copies of
the database next to it) were added on 2026-11-13. The order of the next work was agreed the same day (section 5).
The duplicates CSV was designed on 2026-11-13/14 and taken ahead of that order (schema version 7); a gap found on
2026-11-14 (a second export shows decided groups blank) led to a redesign on 2026-11-16 (Duplicates: `filo delete
FILE.csv`, no stored decisions to delete, four kinds of keep), built on 2026-11-15/16 in five slices (schema version
8). The terminal output was designed on 2026-11-15 and built first (Terminal output). Named retention policies
(schema version 9) and directory rules (schema version 10) were designed and built on 2026-11-16/17. A security review
raised six issues on GitHub (#13-#18), fixed on 2026-11-19 on branch `security-review` (see "Security review fixes"),
and two more (#21, #22), fixed the same day on branch `security-fix` (see "Security fixes: #21 and #22").
Last updated: 2026-11-19.

## Build log

The iterations and the items of the roadmap as they were built, moved from `design.md` section 5 on 2026-11-17.
Item numbers are the roadmap's. Section numbers and names are those of `design.md` at the time: section 5 was this
list, section 6 the open questions (now in `roadmap.md`), section 7 "Rejected or deferred" (now `design.md`,
Rejected, and the deferred ideas in `roadmap.md`; the deferred revert is roadmap item 27a).

1. **Scan, register, report** (read-only). Done 2026-11-11. Done when: on a generated test tree, every kind of change (rename, move,
   edit, delete, duplicate, symlink, unreadable directory, unreadable file) is reported exactly on rescan
   (`scripts/testtree.sh`, checked by the walk and reconcile tests); a first scan of the real home
   directory completes and shows the size of the backlog; a second scan takes seconds to a couple of minutes.
2. **Lifespan: one keep-until date per file**, set through the retention CSV (reshaped 2026-11-12 from a review
   queue). Done when: on a test tree, an export, edit and update round trip records exactly the dates set, refuses
   with one clear line each a changed file, an impossible or ambiguous date, a stale export and an added row; a
   cleared date asks first; a file past its date is listed by `filo delete`, and once edited needs a new date instead
   (`TestRetentionRoundTrip` in `internal/cli`, plus the store and retention package tests). The second shape of the
   CSV (`file_date`, `retention`, display-only `keep_until`, dates from file names, past dates allowed; schema
   version 5) is done, and the round trip covers periods, `file_date` from names, the display column and clears.
   Still to do: on the real home directory, every file gets a retention or `forever`, a directory at a time (filter
   on `directory`, fill down), through a few directory-sized exports.
3. **Delete.** Done 2026-11-13 on a test tree; not yet run on real files. `filo delete [DIR] [-y]` deletes the candidates after the prompt, with the re-hash check, deleted rows,
   the "freed" line in the report and empty directories removed (see Disposal). Done when: on a test tree, a confirmed
   run deletes exactly the unchanged candidates; a file changed, unreadable, replaced or moved after its retention was
   set is skipped with one clear line each; directories emptied by the run are removed up to the root but not an empty
   directory that was already there or one holding a hidden file; a restored file comes back as a new row with the hint;
   `-y` and no-terminal behave as designed. Checked by `TestDeleteRoundTrip` and `TestDeleteInterruptedAtPrompt` in
   `internal/cli` and the `internal/dispose` tests, and by hand on `scripts/testtree.sh` with a changed and an
   unreadable candidate.
4. **Register snapshots.** Done 2026-11-13 (see Register snapshots): `TestKeep` and the snapshot tests in
   `internal/store`, `TestSnapshots` in `internal/cli`, and by hand with a throwaway database.
5. **Duplicates CSV** (taken ahead of the order below on 2026-11-14; see Duplicates). Done when: on a test tree, an
   export, edit, update and delete round trip deletes exactly the copies marked Y; refuses an all-Y group, a partly
   decided group, a sorted row, a moved or changed copy and a Y with a longer retention than every N; asks before
   recording anything refused or any separate content; marks a content separate and undoes it; and `filo delete`
   skips a group whose kept copy was deleted or changed after the update (`TestDuplicatesRoundTrip` in
   `internal/cli`, plus the store tests).

**Next, in order** (agreed 2026-11-13; revert, the earlier iteration 4, was deferred, see section 7; items 1 and 2
put first on 2026-11-15). Each is an open question below until it is designed.

1. Terminal output (designed 2026-11-15, see Terminal output): `internal/view` and its components, then the commands one
   at a time (`delete`, `update`, `report`, `scan` and `roots`, `export` and help). `internal/view` and `delete` done on
   2026-11-16 (component tests in `internal/view`, golden files `internal/cli/testdata/delete-*.txt`); `update`,
   retention and duplicates CSVs both, done on 2026-11-16 (golden files `testdata/update-*.txt`; every question in
   `delete` and `update` goes through one helper, `askChoice`). `report` done on 2026-11-16 (golden file
   `testdata/report.txt`: every section, an empty one as one line ending in "none" (Roots and Register snapshots say
   "none yet" with what to do; Duplicates says "no groups" when it still lists separate or marked contents); recent
   events as a list with scan and event columns). Done when each command's mocked scenarios have golden tests of the
   plain output, and read as designed by hand in a terminal, a 40-column terminal, piped through `cat` and with
   `NO_COLOR=1`. First, since the duplicates re-plan changes what `update` and `delete` print and can then use the
   components. `scan` done on 2026-11-15 (golden file `testdata/scan.txt`: every kind of change with a kind cut at
   ten, a clean directory scan, a disk mounted inside the root, every root with one unmounted; the progress line in
   `TestProgress`; reviewed 2026-11-15). `roots` done on 2026-11-15 (golden file `testdata/roots.txt`: no roots,
   add with --no-scan and with a mount point, taking in four roots with one now skipped, the list, set covering more,
   unchanged and skipping more; `TestRootsAddInterrupted` for Ctrl-C in the first scan; the report's Roots section
   now shows each root's settings, from the same renderer). `export` done on 2026-11-15 (golden file
   `testdata/export.txt`: a retention export with a file_date read from a name, nothing to export, a duplicates
   export with a path that needs quoting, `--separate` with nothing marked; see Terminal output, Export). Help and
   errors done on 2026-11-15 (golden files `testdata/help*.txt`, one per page, wrapped at 80 columns; usage errors
   one line with their help in `TestUsageAndHelp`; `TestErrorLine`; `TestOutputGoesThroughTheRenderer`). Nothing
   prints outside the components now, and `safeText` is retired: the terminal output is done.
2. Duplicates redesign (2026-11-16, see Duplicates). Done on 2026-11-16: `filo delete FILE.csv` carries out a duplicates CSV with no
   stored decisions to delete; the `action` column (`delete`, `keep`, `always`); records and always; `filo
   duplicates` with excluded directories; schema version 8. Done when every scenario in its table has a test, and the
   README explains the four kinds of keep and the rules. After item 1, so its output uses the components.
   Planned on 2026-11-15, in five slices, each with its scenario tests written first, from the table: (1) the
   register, schema 8 (the tables, the new group definition, the report's "Not counted" line; the first design's
   marks, `filo update` on a duplicates CSV, the marked copies in `filo delete` and `--separate` go in the same slice,
   since their tables go; F2, the upgrade); (2) the CSV (`action` column, `keep` filled in for records, reading the
   words, the group rules, the structure checks and refusing an older or a retention CSV; C2, C3, C5, F3); (3)
   `filo delete FILE.csv` (the check against the disk, the retention rule, the kept-copy check at the moment of
   deleting through `dispose`, recording records and always, the output as mocked; A, B, D, E, C1, C4); (4)
   `filo duplicates` list, `exclude` and `include` (mockup to agree first); (5) README, help and status. Between
   slices 1 and 3 duplicates cannot be deleted at all (accepted: the real-data run waits for the redesign anyway);
   slices 1 to 4 stay on one branch, so `main` never sits in that gap nor points at a `filo duplicates` not yet
   built (changed from 1 to 3 after the review of slice 1: its messages name `filo delete FILE` and `filo
   duplicates`, true only once those slices land). The branch's build is not run on the real register before slice
   3: the upgrade's warning is shown once, and its advice (export again to decide) can only be followed from then. Slice 1 done on 2026-11-15
   (`TestDuplicateGroups` for the group definition, F2 and F3; `TestMigrateFromV7` and `TestMigrateFromV7Quiet` for
   the upgrade; `TestNotCounted`, `TestAbsorbMovesExclusions`; `TestUpdateRefusesDuplicatesCSV`; and by hand: a
   register made by the release before, with a mark and a separate content, upgraded by this build, the warning
   once, nothing deleted). The tests of the first design's flow went with it; their scenarios return in slices 2
   and 3, the bind-mounted copy in the check at the moment of deleting among them. Reviewed the same day: a copy is a record if any of
   its paths is (hard links held as two file rows, and bind-mounted paths merged by the export, which then decides
   again which contents are groups), and "not counted" counts only what would otherwise be a group. Slice 2 done on
   2026-11-15: `duplicates.ReadCSV` refuses a CSV of the first design, a retention CSV and one without an action
   column; `duplicates.Plan` applies the group rules and the structure checks to the rows, against the groups the
   register recorded for each export (`store.ExportedGroups`), and notes each group's exported copies with no row in
   the CSV for slice 3's check against the disk (C1). Tests: `TestParseAction`, `TestPlanGroupRules` (every row of
   the rules table, C3), `TestPlanStructure` (C2, C5, C1), `TestWriteCSV` (F3), `TestReadCSVRefusesOtherCSVs`, and
   `TestDuplicatesCSVRoundTrip` from export to plan; the export's words as agreed (Duplicates, Output). A CSV may
   hold groups of several exports, each row checked against its own (agreed 2026-11-15); and it may be partial: groups
   deleted from it are not planned and blank ones are left alone, so a CSV can be worked through and run several
   times.
   Slice 2 reviewed on 2026-11-16: a row listed twice now refuses every group it is listed in and its exported
   group whichever row comes first (before, with the original first, its group was still decided: sorting changed
   the outcome); both orders are in `TestPlanStructure`. And a file given different actions in two exports in one CSV now refuses
   both groups (B3 in `TestPlanStructure`).
   Slice 3 done on 2026-11-16: `filo delete FILE.csv` (`internal/cli/dupdelete.go`; a regular file as the argument
   is a duplicates CSV, a directory is DIR as before), with `store.ExportedCopies` (each exported copy and its state
   in the register now), `store.RecordSet` and `store.AddAlways`, and `fsid.Links` for the hard-link rule. A run
   exits 1 when the CSV refuses a group (as `filo update` does for a refused row), 0 when groups are only skipped.
   Tests: `TestDuplicatesDeleteRunTwice` (A1, A2), `TestDuplicatesDeleteSeveralCSVs` (B1-B3),
   `TestDuplicatesDeleteEditedCSV` (C1 both ways, C4), `TestDuplicatesDeleteChangedFiles` (D1-D4, an unreadable
   copy kept and an unreadable directory), `TestDuplicatesDeleteInsideARun` (E1-E4), `TestDuplicatesDeleteSameFile`
   (a bind mount after the export, a hard link), `TestDuplicatesDeleteAsking`, and the golden files
   `testdata/delete-duplicates-{yes,again,select}.txt`; checked by hand on a throwaway tree, run twice. Reviewed
   the same day, all nine findings fixed (`TestDuplicatesDeleteReviewFindings`, and a hard link made during the
   check in `dispose`'s `TestDeleteDuplicate`): one group in two overlapping exports with the same actions is
   carried out once (the later says "the same as group N of export M"); a refused group exits 1 also when nothing is
   done; records asked for with one copy to keep left say "nothing to record"; the hard-link rule is checked again
   at the last look (`dispose.OnlyLink`; where links cannot be counted, nothing is deleted); an argument that is not
   a directory is a CSV, so a mistyped name fails as "open FILE"; readability and hard links are checked only for a
   group with something to delete, and one readable copy to keep is enough; groups are matched in one pass; the
   "moved since the export (now X)" path is the first by root and path. The help for `filo delete` still names DIR only (slice 5).
   Slice 4 done on 2026-11-16: `filo duplicates`, `exclude DIR` and `include PATH` (`internal/cli/dupcmd.go`,
   `internal/store/notcounted.go`), as in the mockups under Duplicates, Output. The list shows the contents always
   and the sets of records with two or more copies present, as the report's "not counted" line counts them (a set
   or a content with one copy left keeps nothing out, so it is not listed; it stays stored). `include` on a file
   that is both a record and always undoes the record and says the content is still always; a file not in the
   register is an error ("run filo scan"); a directory that is not excluded but has exclusions under it says so.
   Exclusions are matched by exact path prefix (never as LIKE patterns). Tests: `TestDuplicatesList`,
   `TestDuplicatesExclude`, `TestDuplicatesInclude`, `TestDuplicatesUsage`, and the golden file
   `testdata/duplicates.txt`. Reviewed the same day; nine of ten findings fixed (`TestDuplicatesReviewFindings`):
   `include` finds a PATH no longer on disk by its path alone (`store.Locate`), so the exclusion of a directory since
   deleted or unmounted can still be removed, and a record whose directory went still included; the list counts as the
   report does (a content always is listed as always only; a set with a new copy is a group again, so not listed,
   though `include` still removes it); `include` sees a file's copy as the list does (a hard link of a record is a
   record; copies counted the same way, excluded ones left out); `filo duplicates` has its help page
   (`testdata/help-duplicates.txt`); the "dir or under it" test is one SQL fragment (`atOrUnder`); `store.AbsPath`;
   "1 file … counts". Not changed: `exclude` and `include` count the groups before and after (the design's sanity
   check), and the list counts each exclusion on its own (they are few).
   Slice 5 done on 2026-11-16: the README's Duplicates section (the rule, the four kinds of keep, the group rules,
   retentions across copies, the list and its checks, `filo duplicates`), its status, commands, exit codes and
   roadmap; the help overview lists `delete FILE` and `duplicates`, `filo delete`'s page covers a duplicates CSV
   (golden files `help.txt`, `help-delete.txt`, `help-export.txt`). The redesign is done: every scenario in its
   table has a test (A-F: slices 1-3; the plan's C2, C3, C5 in `internal/duplicates`). Still to do: run it on the
   real register (back up first; the upgrade to schema 8 warns once), then merge the branch.
3. Named retention policies (designed 2026-11-16, see Lifespan, Named retention policies), on branch
   `retention-policies`, in four slices, each with its scenario tests written first: (1) the register and store,
   schema 9 (the tables, policies worked out into `keep_until`, the status *needs a file_date*, `policy_changes`;
   G1, B3, F1, E2's status); (2) the retention CSV (policy names in the cell, `filo update`'s create question, the
   refusals, the export; A1-A3, B1, B2, B4, B5, E3); (3) `filo policies` (list, add, set, move, remove, show and
   their questions; A4, C1-C4, D1, D2, E1, E2); (4) the report's line and count, the README (with the date rule),
   the help. Done when every scenario has a test.
   Slice 1 done on 2026-11-16 (`internal/store/policies.go`, schema 9): `PolicyName`, `AddPolicy`, `Policies`,
   `Policy`, `PreviewPolicy` and `SetPolicy`, `PreviewMove` and `MovePolicy`, `RemovePolicy`, `PolicyFiles`; a file
   on a policy reads with the policy's name as its retention (`FileItem.Policy`, `PolicyValue`), also in the
   duplicates CSV; the status *needs a file_date*; `SetRetention` (filo delete's prompt) takes a file off its policy.
   `RemovePolicy` gives the deleted and missing files still on the policy its value as a retention of their own, so a
   missing file that comes back keeps its keep_until. `files.policy_id` is not declared a foreign key (ALTER TABLE
   cannot drop such a column, which the upgrade tests need); the store keeps it pointing at a policy. Tests:
   `TestPolicyName`, `TestAddPolicy` (A4), `TestPolicyKeepUntil` (B3, F1, E2's status), `TestSetPolicy` (C1-C4 in the
   store), `TestMoveAndRemovePolicy` (D1, D2), `TestSetRetentionLeavesThePolicy` (B4 in the store),
   `TestMigrateFromV8` (G1); by hand, a register made by the `main` build upgraded by this one (schema 9, the
   pre-upgrade snapshot). Reviewed the same day; nine of ten findings fixed (`TestPolicyReviewFindings`,
   `TestDuplicatesDeleteNeedsFileDate`), the tenth (a policy file's name in the CSV is refused by `filo update`) is
   slice 2's: `filo update` setting a retention of its own takes a file off its policy too; `SetPolicy` and
   `MovePolicy` count present files (as the previews do) and the history counts the dates changed; removing a period
   policy gives a missing file with no file_date no retention, rather than a period with nothing to count from;
   policies are read inside the transaction that changes them; a `Filter.PolicyID` replaces loading every file; one
   SQL spelling of a file's retention (`retentionColumns`), so the duplicates code sees the policy's value; policy
   names are checked in ASCII before lowering (no Kelvin sign as k); the preview leaves the status out. **In the
   duplicates rules** (agreed 2026-11-16): a copy to delete that needs a file_date refuses its group ("needs a
   file_date first"), since its retention's end is not known.
   Slice 2 done on 2026-11-16: a policy name in the retention CSV (`retention.ParseRetention` returns it in lower
   case; `store.IsPolicyRef` tells it from a period, a date and `forever`); `store.UpdateRetentionWith` puts the file
   on the policy and works keep_until out from its value, lists the new policies (`NewPolicy`) and creates them only
   if `ConfirmPolicies` says yes (declined, or no terminal: those rows are left alone; a dry run lists them); a row
   naming a period policy with no file_date is refused, which also refuses clearing the file_date (E3); the stored
   retention is read as the policy's name, so an export of a policy file updates untouched (the review's tenth
   finding). `filo update` shows the "New policies" block and asks `Create 2 policies? [y/N]` before the clears.
   `filo delete`'s new-retention prompt takes a policy name too: an existing policy puts the file on it, an unknown
   one is refused there ("no such policy"), since that prompt does not create one; `KeepUntil` refuses a name, as a
   safety net. Every policy name is a valid cell now, so a lone `y` or `indefinitely` would be a new policy: the
   question catches them. Tests: `TestParseRetention`, `TestUpdateRetentionPolicies` (A1, A2, B1, B2, B4, B5, E3),
   `TestSetRetentionPolicy`, `TestUpdatePolicies` (golden `testdata/update-policies.txt`). Reviewed the same day,
   all ten findings fixed: a file_date suggested from the name is kept for a policy whose value is a period (the rule
   looked at the name, so such rows could never apply); `filo update` counts the files put on a policy with no value
   apart ("they need a retention until it has one"); filo delete's prompt says when a policy has no value yet, shows
   policy names in its refusal, and treats only "no such policy" and "needs a file_date" (`NeedsFileDateError`) as
   refusals, any other error ending the command; an update looks each policy up once (`policyCache`) and keeps its
   ID for the write; `policyJoin` with `retentionColumns` is the one spelling, used by `filo update`'s check too.
   **In the duplicates rules** (agreed 2026-11-16): a copy to delete on a policy with no value yet refuses its group
   too ("its policy payslip has no value yet"). Tests: new cases in `TestUpdatePolicies`, `TestDeletePromptPolicies`,
   `TestDuplicatesDeleteNeedsFileDate`.
   Slice 3 done on 2026-11-16: `filo policies` (`internal/cli/policies.go`), as mocked: the list (a `view.Table`:
   name, value, "from each file's file_date" for a period, files, and a `!` note: needs a retention, passed, N without
   a file_date; the heading counts those needing attention), `add`, `set` (a change with nothing to ask about is one
   `Done` line; shortening that puts files past their date, or a period leaving files without a file_date, shows them
   and asks; `-y`; no terminal changes nothing), `move` (asks for a rename too: `Rename bils to bilz (1 file)?`; a
   merge shows what it would do to dates and ends `Next: filo policies remove OLD`), `remove` (refused in one line
   while in use), `show` (files needing a file_date first, with the `Next:` to fix them). A passed date policy with
   files is flagged too ("its files are past their retention"). The help page `filo help policies`; the overview
   waits for slice 4. Tests: `TestPoliciesAdd` (A4), `TestPoliciesList` (E1, E2's flag), `TestPoliciesSet`
   (C1-C4), `TestPoliciesMoveRemove` (D1, D2), `TestPoliciesShow`, `TestPoliciesUsage`, golden
   `testdata/policies.txt` and `help-policies.txt`. Reviewed the same day: seven of eight findings fixed
   (`TestPoliciesReviewFindings`); the eighth (hard links counted twice) does not apply, since a hard link is a file
   row of its own. A move onto the same policy (`bills` to `BILLS`) is refused; a question after a cut list offers
   `l` (`askYesNo`, now also behind `filo update`'s two questions; `confirmChange` for the policies ones); a merge
   into a policy with no value lists the files that would lose their keep_until (`PolicyPreview.LoseDate`) and
   asks; one wording for a policy with no value ("no value"); the list works out today once.
   Slice 4 done on 2026-11-16: the report's Retention section names the policies whose files need a file_date (`! 1
   file needs a file_date: its policy counts from it (bills, 7y)`; the status reads "no file_date for their policy"
   in the needs-a-retention count) and ends with `N policies, M need attention (filo policies)` when there are any
   (`report.PolicyAttention`, shared with `filo policies`); the help overview lists the policies commands, `filo
   delete`'s page states the date rule, the export page names policy names; the README has a "Retention policies"
   section and the date rule. Test: `TestReportPolicies`; goldens `help.txt`, `help-delete.txt`, `help-export.txt`.
   Reviewed the same day (the whole branch), all nine findings fixed (`TestOldCSVAfterMove`,
   `TestReportPoliciesReviewFindings`): a `move` or rename records a history row per file (`reviews`, batch
   `policies`), so a row of a CSV exported before it and edited since is refused ("changed after export"), and an
   old CSV never puts a file back on the old name; the report's policies line shows with no files too, marked `!`
   when a policy needs attention, and names the policies of exactly the files it counts as needing a file_date; a
   `set` question names both what it puts past and what it leaves without a file_date. **Named retention policies are
   done**: every scenario in the table has a test. Next: merge; then directory rules (item 4).
4. Directory rules: a policy for the files under a directory, applied by every scan (designed 2026-11-16, see
   Lifespan, Directory rules; schema 10). Four slices on branch `directory-rules`, one PR when all four are done, so
   it brings a complete feature. Scenario tests first in each slice.
   1. *Register and matching*: schema 10; matching in a new package `internal/rules` (globs on the name in any case,
      letter count, nearest directory first, ties reported); the store adds, changes, removes, moves and copies rules
      and dates files by them; rules get a root's prefix when a root is added above it; `filo policies` remove and move
      take rules into account. Nothing calls the rules yet. Scenarios C1-C3, D3, G1, F3. Done on 2026-11-16:
      `internal/rules` (`Pattern`, `Letters`, `Match`, `Resolve`, `Compare`; a `[` or `\` in a pattern is itself (a `\`
      refused since 2026-11-17, and `Letters` is now `Length`, see after slice 3), and `**` is `*`); schema 10
      (`TestMigrateFromV9`); `store.AddRule`, `SetRule`, `RemoveRule`, `Rules`, `MoveRules`
      (move and copy), each of the three that apply rules with a preview that runs the same work in a transaction it
      rolls back (`PreviewAddRule`, `PreviewMoveRules`); dating in `store/ruledate.go`. Tests: `TestResolve`,
      `TestCompare`, `TestMatch`, `TestAddRule`, `TestSetAndRemoveRule`, `TestMoveRules`, `TestPoliciesAndRules`,
      `TestRuleDating`, `TestRuleTies`, and the absorb test (F3). Reviewed the same day, nine of ten findings fixed (one
      of them, hard links, undone the same day: it took a hard link for one file with two paths, while Filo keeps each
      link as a file of its own; the person chose each link dated by its own directory's rule, C4); files under a
      directory the latest scan covering it could not read left alone by `add`, `move` and `copy` too
      (`TestRuleDatingUnreadDirs`); a rule's directory checked for the register's form and its root for being
      registered; a move up allowed; `*?` stored as `?*`; a root's rules indexed once, and `add` reading only its
      directory. The tenth (the duplicate check before the insert repeats the unique constraint) kept: it gives the
      one-line error, with the same key. Slice 2 must refuse `add`, `move` and `copy` in a root that cannot be reached
      (not mounted), which the store cannot see.
   2. *`filo rules`*: the list (`[DIR]`, the order tried, counts, flags), `add` (a policy created if needed,
      `--dry-run`), `set`, `remove`, as mocked (agreed 2026-11-16), and the help page; refused in a root that cannot be
      reached. Scenarios A, B4, D, F1.
      Done on 2026-11-16: `internal/cli/rules.go`, `view.Groups` (a table of rows under directory names),
      `store.RuleTies` (the list's "Not applied"); the help page `filo help rules`. `set` and `remove` say the files
      "keep the policy it set", not the rule's old policy by name: a rule set more than once may have dated
      them with different ones. `add`'s line ends with the root, as every heading does. Tests: `TestRulesGolden`
      (`testdata/rules.txt`), `TestRulesAdd`, `TestRulesSetRemove`, `TestRulesListScope`. Not tested: refusing a
      root that is not mounted (it is `store.Resolve`'s, and needs a real mount).
   3. *Applied by every scan*: the scan's sections on what it set and did not (as mocked, agreed 2026-11-16); `filo
      report` counts the files with a policy set by rules, lists the ties and suggests `filo rules add`. Scenarios B,
      C2, F2.
      Done on 2026-11-16: `store.ApplyScanRules` applies the rules in the scan's transaction, after its changes, to
      every file in its scope with no retention, stamped with the scan (`ApplyScan` does the same, returning the scan
      ID only); `reconcile.Summary.Rules`; the scan's count lines, "Policies set by rules" (`view.HeadedTable`) and
      the ties; the report's count, ties and next step; a retention set by hand (CSV, `filo delete`, a removed
      policy's value) takes a file off its rule (`files.rule_id` cleared, the mark kept); `FileItem.RuleID`. In the
      report the tie line comes after "Where most need one", which stays right under the line it explains. Tests:
      `TestScanAppliesRules`, `TestRetentionByHandLeavesTheRule`, `TestScanSummaryRules`, `TestHeadedTable`,
      `TestScanRulesGolden` (`testdata/scan-rules.txt`), `TestReportRules`.
      Reviewed the same day, six of eight findings fixed: `filo update` (and a retention given at `filo delete`)
      keeps a file on its rule while its policy stays the rule's, so filling in a `file_date` does not take it off;
      applying the rules reads only the files with no retention (`Filter.Ruleless`) before classifying, so a scan
      holds the write lock for those only; one tie wording for scan and report (`report.TieWhy`); the report's tie
      rows name the rules' directory; a scan whose only finding is a tie gets a next step. Kept: `ApplyScan` beside
      `ApplyScanRules` (both apply the rules; the tests want the scan ID only), and the headed table's trailing
      padding (`write` already drops it).
      Changed after the PR review of slices 1-3 (2026-11-17): a `?` counts toward a pattern's length (only `*` does
      not), and a `\` in a pattern is refused as a `/` is; each refusal of a pattern names the check that failed.
   4. *`move` and `copy`*, then the README (a "Directory rules" section, quoting patterns) and the help overview.
      Scenarios E.
      Done on 2026-11-17: `filo rules move OLD NEW` and `copy OLD NEW` with `--dry-run` (`store.MoveRules`,
      `PreviewMoveRules` from slice 1), said as agreed: `Done   moved 2 rules from bank to banking · policy set on 1
      file`, a dry run as `add`'s (`would move`, the files, "nothing was changed"). OLD may be gone from disk (found
      by its path, `store.Locate`: the rename that calls for a move); NEW must be on disk in a root that can be
      reached. With two roots both directories are named in full. A refusal because NEW or a directory below it has
      rules lists them under their directories before the error line. The help page and the overview list both;
      the README has a "Directory rules" section. Tests: `TestRulesMoveCopy` (E1-E3, a dry run, another root, the
      arguments), `TestRulesMoveGolden` (`testdata/rules-move.txt`). **Directory rules are done**: every scenario in
      the table has a test. Next: the PR, then removing empty directories (item 13).
11. Replacing an existing `-o` file after asking (decided 2026-11-16, see Lifespan). Done on 2026-11-16:
    `TestExportReplace` (no terminal, no, Enter, yes, a directory, a symlink, a device, `-y`) and
    `TestWriteExportReplace` (a failed write and a file changed after asking leave the old file as it was).
12. Every export scans first, `--no-scan` to skip it (decided 2026-11-16 for `export duplicates`, widened the same
    day to `export retention`; see Lifespan, "A scan first"). Done on 2026-11-16: `TestExportScansFirst` (a file
    saved since the last scan, DIR only, every root for duplicates, `--no-scan` and its line, a failed root, Ctrl-C)
    and the golden file `testdata/export.txt`.

## Duplicates: how the redesign was built

**Built** (on branch `duplicates-redesign`, in five slices, see section 5). Slice 1, the register: schema 8, the
group definition below, the report's "not counted" line; the first design's `filo update` of a duplicates CSV, its
marks in `filo delete` and `--separate` are gone. Slice 2, the CSV: the `action` column, `keep` filled in
for records, and reading it into a plan per group (`duplicates.Plan`). Slice 3: `filo delete FILE.csv` carries a
duplicates CSV out. Slice 4: `filo duplicates`, `exclude` and `include`. Slice 5: the README and the help. See "The first design, and why it changed" at the end
of this section.

## Duplicates: the first design, and why it changed

**The first design, and why it changed** (2026-11-13/14, built; redesigned 2026-11-16). `filo update` recorded each
  group's Y/N decisions as marks in the register (two or more N marked the content separate, everywhere), and
  `filo delete` deleted the marked copies later, alongside retention candidates. A second export then showed decided
  groups blank, though the marks were stored. A fix that filled the cells in from the register (branch
  `fix-duplicates-includes-copy-decision`, kept at a4ffe5f) went through six code review rounds, each finding new
  cases, and was abandoned on 2026-11-15: the same decision lived in three places (the register, the CSV and the
  disk), each able to change on its own. Designing from the flow and its scenarios instead led to keeping no
  decisions to delete at all, and to splitting "separate" into records (these copies) and always (the content), since
  insurance terms kept as two records should not make every future copy of them acceptable.

## Schema versions

`ctime_ns` and `unreadable_since_scan` are schema version 2,
the first migration (two `ALTER TABLE ... ADD COLUMN`s; existing rows start with ctime 0, so the first scan after the
upgrade rehashes nothing extra and records each file's ctime quietly). Version 3 adds the review columns on `files`
(all NULL for existing rows, so every file starts undated) and the four review tables. Version 4 (2026-11-12) merges
`review_date` and `retain_until` into `keep_until`, taking the later of the two so nothing is kept for less time; a
recorded `delete` becomes `keep_until` = the day it was decided, so the file is a deletion candidate; the same in
`reviews`; `export_rows` loses `reason` and gains `keep_until`; `import` batches are renamed `update`; roots gain
their settings (defaults for existing roots: hidden skipped, no extra ignores). Columns are dropped with `ALTER TABLE
... DROP COLUMN`. Version 5 (2026-11-12) adds `file_date` and `retention` to `files`, `export_rows` and
`reviews`; each existing `keep_until` becomes the retention it was (its date, or `forever`). Version 6 (2026-11-13)
adds `files.deleted_utc` (the deleted rows are the history of deleted files) and `scans.kind` (existing scans are
`scan`); see Disposal. Version 7 (2026-11-14) adds `exports.kind` (existing exports are `retention`),
`export_rows.dup_group` and `path_id`, and the three duplicates tables; see Duplicates.
Version 8 (2026-11-15) is the duplicates redesign: it drops `duplicate_marks`, `duplicate_reviews` and
`separate_hashes` (each separate content becomes always, in `always_hashes`), deletes the review batches only they
used, and adds `duplicate_records` and `excluded_dirs`; the run that upgrades warns once about decisions not carried
out. Version 9 (2026-11-16) adds `files.policy_id` and the tables `policies` and `policy_changes`. Version 10
(2026-11-16) adds `files.rule_id`, `files.rule_dated_utc` and the table `rules`.

## Duplicates: showing the decisions recorded

An open question raised 2026-11-14 and resolved 2026-11-16 by the duplicates redesign: nothing waits in the register
to be shown (see "the first design, and why it changed" above). Points the abandoned branch
(`fix-duplicates-includes-copy-decision`, a4ffe5f) found that the redesign's build had to keep in mind: records are
matched by hash, so a file restored to the content it was kept on would rejoin its set; a copy with hard links is one
copy, named by its first path; `created_utc` strings do not sort as times (order by ids); the same content in two
groups of one CSV (rows pasted from two exports) must not give contradictory actions.

## Security review fixes

A security review of the code raised six issues on GitHub (#13-#18). They were fixed on 2026-11-19 on branch
`security-review`, one commit per issue, no schema change; the design is in `design.md` (Register, Retention CSV,
Scan, Disposal), each part naming its issue.

- **#15, the database path** (`a9ba70f`): the database's directory is resolved before the lock is taken; the database
  and its WAL, SHM and journal files must be regular files owned by the user, not symlinks (modernc's SQLite has no
  way to open with `SQLITE_OPEN_NOFOLLOW`, so the check is an `lstat` just before the open). Tests:
  `TestDBFilesChecked`, `TestDBDirSymlinkResolved`. Untested: the refusal of a file owned by another user (a test
  cannot make one).
- **#18, a bounded CSV reader** (`0a1ed61`): `sheet.ReadAll` (at most 256 MB read) and limits checked before any row is
  parsed: 1,048,576 rows and 16,384 fields (a spreadsheet's maximum), 1 MB a row. Tests: `TestReadAllLimit`,
  `TestLimits`, `TestReadCSVLimits` (retention and duplicates).
- **#16, the CSV read once** (`4927312`): `sheet.ReadFile` (refused from its size) and `ParseCSV` in both parsers, in
  place of `os.ReadFile` and a second copy in `ReadCSV`. Tests: `TestReadFile`, `TestCSVTooLarge` (update and
  delete).
- **#14, the walk** (`d5b6d6e`): the issue said `statID` follows symlinks; it is an `lstat`, and the directory was
  followed by `os.ReadDir`. The walk now opens each directory through the one above it (`os.Root`) and checks it is
  the one `lstat` saw; a scan's hash jobs carry their root (`hasher.Job.Root`, `OpenIn`). Error messages keep the
  absolute path (`open /…`, not `openat rel`). Tests: `TestDirSwappedForSymlink` (the regression test the issue
  asked for, through a `beforeOpen` hook), `TestJobInRoot`.
- **#13, deletion** (`6b98d95`): the file's directory is opened from the root down and every check and the removal go
  through it. On Linux the removal is `unlinkat` by a raw syscall, since the standard `syscall` package has it only
  without flags (`golang.org/x/sys/unix` would be cleaner, but it would be a new direct dependency); other Unix
  systems use `os.Root.Remove` after an `lstat` (untested). Tests: `TestDeleteDirSwappedBeforeUnlink` (the old code
  was shown to delete the file outside the root), `TestPruneDirSwapped`.
- **#17, Go 1.27.4** (`bd5a6dc`): `go.mod`, the container image and the README. With `GOTOOLCHAIN=local` the 1.27.3
  container refuses the new `go.mod`, so the container must be rebuilt; the checks from this commit on were run with
  the 1.27.4 toolchain downloaded into the module cache.

**Review** (`code-review`, level medium, over the six commits): nine findings, seven fixed in three commits.

- Sheet (`44139af`): the limit check flipped its quoted state on every quote, so after a bare quote (`12" ruler`) it
  stopped counting rows and named the wrong error; it now follows csv's quote rules and leaves a quote csv refuses to
  csv. Tests: new cases in `TestLimits`.
- Walk and hash (`b317e7b`): a directory gone between its `lstat` and its opening was taken to be gone, so its files
  could be marked missing (now skipped as changed); a root or scope that cannot be opened stopped the walk (a failed
  listing again, as before); hashing opens each file's directory once per worker run of files, then the file by its
  bare name, instead of two whole-path lookups; `OpenIn`'s comment overclaimed (a directory swapped for a symlink
  staying inside the root is followed). Tests: a dangling-symlink case in `TestDirSwappedForSymlink`,
  `TestScopeCannotBeOpened`.
- Dispose (`a108ac7`): a directory moved out of the root while the file was hashed, with a new one made in its place,
  left the open directory outside the root and the file was unlinked there; the last look now checks that every
  directory from the root down is still the one opened (`heldDir.stillAt`), and the pruning does too. The duplicate
  guard's hard-link count was taken before the kept copy's look; it is live again. Tests:
  `TestDeleteDirMovedOutOfRoot`, `TestDeleteDuplicateLinkAfterKeptLook` (an `afterKeptLook` hook). Both tests, and
  the walk's dangling-symlink case, were checked to fail on the code before the fix.
- Not changed: a directory that can be entered but not read (mode 711) now makes `filo delete` refuse the files under
  it and flag them unreadable, where it used to delete them; nothing is deleted, and a scan cannot list such a
  directory either. And the go 1.27.4 requirement in the old container, which is #17's purpose.

## Security fixes: #21 and #22

Two more issues from the security review, fixed on 2026-11-19 on branch `security-fix`, no schema change; the design
is in `design.md` (Register, Scan, Disposal, Accepted risks).

- **#22, the database directory** (`a78b4e6`): the issue asked for a descriptor-based open; modernc's SQLite opens the
  database and its companion files by path, so the gap between the check and the open is closed by refusing a
  database directory not owned by the user or writable by the group or others, before the lock is taken. `--db
  /tmp/f.db` is refused, so the dev examples (CLAUDE.md, README, `testtree.sh`) moved to `T=$(mktemp -d)`. Test:
  `TestDBDirOthersCanWriteRefused`.
- **#21, the file swapped before the unlink** (`4aad35c`, `ce24731`): discussed first. Anyone who can swap the entry
  can already delete it, so the case that matters is an editor or Syncthing renaming a new version into place in that
  instant, which Filo would delete unseen. Rejected: renaming the file in place (`notes.txt.RANDOM`; the new name is
  still in a directory others can write to) and `chmod 0700` on the file (a file's mode does not protect its name).
  Built: the file is moved into a private directory next to it (`/tmp` would be another filesystem), checked there,
  unlinked, and the empty directory removed (no recursive delete); on a mismatch it is moved back with
  `renameat2(RENAME_NOREPLACE)`, a raw syscall with its number per architecture taken from `golang.org/x/sys` (not
  added as a dependency). Putting it back rather than leaving it hidden: Syncthing would delete a hidden file
  everywhere. A scan reports a leftover `.filo-delete-*` directory and never walks it. Tests:
  `TestDeleteFileSwappedBeforeUnlink` (the issue's regression test, through `beforeUnlink`; checked to fail on the
  old plain unlink), `TestPrivateDirSwapped`, `TestDeletingDirReported`.

**Review** (`code-review`, level medium, over the three commits): five findings, four fixed in one commit.

- Dispose (`3ceda4d`): a full disk or used-up quota made every delete fail (the private directory could not be made),
  and so did filesystems without Unix modes (the 0700 directory came out 0755 or 0777); the directory now needs only
  to be the user's and not writable by others, and where none can be had the file is unlinked at its name as before
  #21. A private directory refused that way was left behind (now removed); a file checked that could not be unlinked
  in the private directory stayed hidden (now put back). The Linux-only tests did not build on other platforms (now
  in `dispose_linux_test.go`). Tests: `TestNoPrivateDir`, `TestUnlinkInPrivateFails`.
- Not changed: a group-writable database directory is refused even when its group is the user's own private group
  (Debian and Ubuntu with umask 002); telling that apart needs the group's members, which Go's standard library does
  not give, and the error says `chmod go-w`.
- Untested: everything on platforms other than Linux on amd64, and a real FAT or network filesystem (simulated by
  changing the directory's mode).

## Notes on decisions

Short notes taken out of `design.md` on 2026-11-17, by the section they were in.

### Lifespan

- Decided 2026-11-12, replacing the review queue, review dates, actions and retain-until floors of 2026-11-11.

### Register snapshots

- Rotation reworked 2026-11-13 from a first draft that kept only the newest three of the day, which was fragile around
  midnight.

### Roots

- Root settings (2026-11-12): until then they were flags on each scan, and a scan without `--include-hidden` quietly
  left hidden files unchecked.

### Exports

- Every export scans first: decided and built 2026-11-16.
- An existing `-o` FILE replaced after asking: decided and built 2026-11-16; until then an existing FILE was refused.

### Retention CSV

- `filo update` used to have an "already imported" rule, which silently ignored a re-edited row; it was dropped on
  2026-11-12.
- The second shape of the retention CSV (decided and implemented 2026-11-12, schema version 5) replaced the single
  `keep_until` input of the first shape.
- Past dates accepted since 2026-11-12; the update used to refuse a date not after today.

### Named retention policies

- Settled with the person one topic at a time on 2026-11-16 (it had been a proposal in Open questions since 2026-11-13).
  Built on branch `retention-policies` in four slices (see the build log).

### Directory rules

- Settled with the person one topic at a time on 2026-11-16 (it had been in Open questions since 2026-11-13). Built on
  branch `directory-rules` in four slices, one PR for all of them (see the build log).

### Disposal

- Decided 2026-11-12, implemented 2026-11-13. It replaced the earlier plan of a quarantine, holds and several checks at
  the moment of acting.

### Duplicates

- Designed 2026-11-13/14; redesigned 2026-11-16, built 2026-11-15/16. On the real home directory the report found 297
  groups, 605 files.
- The export's backups line first said "lists the exclusions" and "always ends with"; changed on 2026-11-15: a list
  repeats `filo duplicates`, and a line on every run stops being read.
- The report's line "N copies marked for deletion" (first design) went with the redesign.
- The report line was "not counted: … 1 set of records (filo duplicates)", which did not say counted as what, and
  "records" was never explained (changed 2026-11-16).

### Commands

- Reshaped 2026-11-12: `review` and its subcommands `set-date`, `export` and `import` were replaced by `export lifespan`
  and `update`; "update" says that only existing files change, which "import" did not. `export lifespan` was renamed
  `export retention` the same day: the CSV's word names what is set in it, and the lifespan CSV became the retention CSV
  (package `internal/retention`). `restore` was dropped the same day.

### Terminal output

- The first design's escaping writer, `safeText` (2026-11-13), was retired when the terminal output components were
  built (2026-11-15/16).
- Designed 2026-11-15, one topic at a time from mockups. Before it, the output worked but read badly, `filo update` and
  `filo delete` most: an absolute path on every line, refusals as dense one-line messages on stderr (`filo: d.csv line
  7: /long/path: group 3 of export 4: reason`), summaries in whatever order the code reached them, no visual weight and
  no consistent next step. Built command by command: `delete`, `update`, `report`, `scan` and `roots`, then `export` and
  help.
- Before 2026-11-15 the help was hand-wrapped at about 115 columns, and one paragraph was a single 216-column line.
- An error with a newline was first designed as "split at newlines"; changed on 2026-11-15 after the code review found
  that let a name forge a line.
