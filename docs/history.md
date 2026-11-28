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

## Showing and setting a file's details: how item 37 was built

Designed with the person on 2026-11-20 (roadmap item 37, with the mockups of every output agreed first), built on
branch `item-37` on 2026-11-21 in six slices, one PR, each slice reviewed once (`code-review`, medium).

1. *`filo show FILE`* (`store.ShowFile`; `view.Subject`, `Part`, `Fields`; `internal/show`; `internal/cli/show.go`).
   Review: five findings, four fixed: a file in a directory filo cannot read is shown from the register, the path
   line saying why (it was an error); a symlink compared as a scan compares it (target and broken state, not ctime:
   `walk.Broken`, shared with the walk); "unchanged" adds "the next scan reads it again" for a file with no hash or
   flagged unreadable; a missing file found through a symlinked way into its root, as one on disk is. The fifth (the
   view commit before the internal package that renders through it) changed CLAUDE.md instead: the view may come
   first when a package renders through it.
2. *`filo show DIR [-r]`* (`store.ShowDir`, `HasFilesUnder`; `view.List`, `Block`; `show.Dir`). Review: seven
   findings, six fixed: a directory on disk the register holds nothing under is an error, as a file is (it showed as
   empty); a directory whose files all moved out is not one with something to show; the files grouped once, not
   gone through per directory (a large root with -r would have taken minutes); a rule on a directory with no file
   yet gets its block under -r; rule ties shown under the rules; the help says which flags are for files and which
   for directories. Kept: a path that is a symlink to a directory is the symlink, an entry of its own.
3. *`filo set`, file date and retention* (`store.SetFiles`; `view.Over`; `internal/cli/set.go`). Review: seven
   findings, five fixed: a moved file's old path no longer sets it ("not at this path any more"); only a path outside
   every root is a refusal, other lookup errors are errors; the clear question counts only the retentions cleared; no
   empty table in a dry run with nothing to change; `filo show` of a missing or deleted file no longer points to
   `filo set`; a path given twice counts once. Kept: the question asked inside the transaction (`filo update` does the
   same). Then the person decided that a policy not there yet is created with no value, without asking, and said so
   (it was refused).
4. *`filo set --duplicates`*. Review: eight findings, all fixed, five of them one cause: the decision was read from
   the named files' own rows, when it belongs to the content as a whole. Rebuilt on that: it was always, or records if
   any copy is one (hard links one copy), or counted; always and counted replace the content's sets of records, and
   ask; records leaves exactly one set, the copies named, so no set of one is left. Also one refusal per file, no
   empty review batch, and the Unchanged heading naming both kinds of flag.
5. *`filo get`* (`show.Fields`, `File.Value`; `view.Line`, `Pairs`, `Record`; `internal/cli/get.go`). Review: nine
   findings, eight fixed: `--duplicates` the content's decision, empty with no content to decide on; `--modified`
   empty when not known; a path's trailing space as `\x20`; with -0 only the path raw. Then the person removed `-0`
   (its rows were no list of names for `xargs -0`, and it was the one name written unescaped): everything `get`
   prints is escaped; NUL-separated names wait for `filo files -0` (item 19).
6. *README and help*: a section "One file at a time", the commands table, `filo set`'s options, the exit codes.
   Review: five findings, all fixed (the exit codes, the get layouts, the item's section in design.md written as
   "as built" notes rather than slices).

Tests: store `TestShowFile`, `TestShowFileRule`, `TestShowDir`, `TestShowDirTies`, `TestSetFiles`,
`TestSetReplacesARulesPolicy`, `TestSetDuplicates`, `TestSetDuplicatesWholeContent`; view `TestSubjectPartFields`,
`TestSubjectResetsTheRoot`, `TestListAndBlock`, `TestFilesOver`, `TestGetOutput`; show `TestTimeLeft`, `TestLook`,
`TestRender`, `TestRenderDir`, `TestLookDir`, `TestValue`; CLI `TestShowGolden` (`testdata/show.txt`), `TestShow`,
`TestShowWritesNothing`, `TestShowDirGolden` (`show-dir.txt`), `TestShowDir`, `TestSetGolden` (`set.txt`), `TestSet`,
`TestSetDuplicatesGolden` (`set-duplicates.txt`), `TestGet`, and the help pages.

## Finding files: how item 19 was built

Designed with the person on 2026-11-21, topic by topic (the name, what `--name` matches, DIR, piped output and
escaping, the terminal list, what is listed, the exit status, the scan first), built on branch `find` the same day in
one slice, one PR, reviewed once (`code-review`, medium). Changed from the roadmap's version of 2026-11-19: the command
is `filo find`, not `filo files` (the person's choice); a terminal shows every match, not 10 and "… N more"; it scans
first, as exports do; and a scan that found nothing now takes no snapshot, so a `find` (or an hourly timer) does not
leave a copy of the register each time. A new item came out of the session: 38, HTML pages listing files by tag,
placed straight after 18a.

Commits, lower layers first: the roadmap (the name, item 38); the design moved in; the store (`commitRecord`, so a
scan that found nothing takes no snapshot; `FindFiles`); `internal/find` (`Match`, `Select`, then `Render`);
`view.PathLines` and `RawPaths`, the second the one unescaped output; `internal/cli/find.go` (`lastScanLine` now
takes a filter, for exports and find); the README.

Review: seven findings, six fixed. A mistyped DIR (a path never in the register) is an error, as for `filo show`
(it scanned its root and printed "no files", exit 0); a symlink given as DIR is refused with what to give instead (it
was called a file); hard links are matched on inode, size and modified time (a stale inode reused by another file hid
it) and counted over the whole root, not only DIR; a byte that is not valid UTF-8 matches only itself in a pattern;
CLAUDE.md and `TestWritesGoThroughTheHelpers` name `commitRecord` as the one commit that takes no snapshot. Not
changed: a failed write to stdout already exits 1 for every command (`Run` checks stdout's first error).

Tests: store `TestNoSnapshotForAScanThatFoundNothing`, `TestFindFiles`, `TestFindFilesLinks`; find `TestMatch`,
`TestSelect`, `TestRender`; view `TestPathLines`, `TestRawPaths`; CLI `TestFindGolden` (`testdata/find.txt`),
`TestFindPiped`, `TestFindRefusals`, `TestSnapshots` (no snapshot after a scan with no changes), and the help pages.
Run by hand on a throwaway tree (`scripts/testtree.sh`): piped, `-0 | xargs -0 ls -l`, a terminal through `script`,
an unreadable directory, and the snapshot count after a `find` with nothing changed.

## Tags: how item 18a was built

Designed with the person on 2026-11-19/20 (one mechanism for tags and projects, `key:value` or a bare value, the
spelling rules, the commands, the tags CSV, single-file registration, tags following the file and its copies), and
built on branch `tags` on 2026-11-22 in six slices, one PR, each slice reviewed once (`code-review`, medium). The
person asked to go from slice to slice without waiting, and to stop only for a question; none came up. The same day
they settled that a project note (18b) is not a tag, and noted an idea: tasks and events on a CalDAV server, each
with a link to its files (Later d).

Settled while building, by Claude, and written into `design.md` (outputs were not mocked with the person first, as
the roadmap had planned, since they asked not to be stopped; to look over): the spelling first typed is the tag's on
every file, and `filo tag` says when it gave a typed tag another spelling; `filo tag` takes the files first, the
arguments on the disk, so a tag that names something in the current directory is read as a file; tag and untag are
all or nothing and do not ask; `untag` and `tags rename` take `key:` for every value; a bare search finds bare tags
only; a file not seen yet is registered as a scan whose scope is the file; the scan says so only when copies' tags
differed; `filo tags` is laid out as `filo policies`; `filo show` lists the tags; `filo update` asks before removing
tags. It had no `-y` at first, as clearing a retention has none; once the item was done the person asked for one
(2026-11-22), and `-y` now answers that question only (with a retention CSV it is a usage error).

Slices and commits, lower layers first:
1. *The register and the spelling rules*: the design moved in; `internal/tags` (`Parse`, `ParseSearch`,
   `ParseRemoval`, `Fold`, `Match`); schema 11 (`tags`, `tag_changes`, `export_rows.tags`) with `TagFiles`,
   `UntagFiles` and `FileTags`. Review: five findings, four fixed (the `*` matcher moved into `tags.Match`, which
   `find.Match` calls, so `--name` and `--tag` match alike; a tag typed twice in two spellings is reported; deleted
   files are refused; a comment gofmt had garbled). Not changed: a key whose lower case is not a key cannot happen,
   as `strings.ToLower` maps a letter to a letter (`İ` to `i`); a test pins it.
2. *`filo tag` and `untag`*: `walk.SkipsPath`; a new file's tags from its copies (`Store.Copies`, `NewFile.Tags`);
   reconcile giving new files their copies' tags, the `copy_tags` event, and possible moves noting the old file's
   tags; `PrepareFile` and `Register`, registering one file by itself; `filo scan`'s line; the commands. Review: nine
   findings, all fixed (a file the scan finds missing is no copy, so tags never cross a move the scan did not pair;
   a copy in another root, perhaps unmounted, no longer refuses a file as moved; one file by two paths is one file;
   a registered file counts as tagged; a file back at its own path is worded so; the copy_tags event has its own
   word; and wording).
3. *`filo tags` and rename*: `TagSummary`, `RenameTag`, the commands. Review: six findings, five fixed (files
   respelled by a rename are counted apart; a key rename respells too; the merge count's wording; a key renamed to
   itself is a usage error; help came in slice 6). Not changed: the three queries of `filo tags` cannot see another
   writer's change halfway, as every run holds the register's lock.
4. *`filo find --tag` and `--no-tag`*: found files carry their id; `find.Query`; tags after the path; a Tags part in
   `filo show`; the flags. Review: eight findings, seven fixed (a file's hard links, each a file in the register,
   are matched on their tags together; filo's own notes go in brackets after the tags; show counts the tags and
   lists one a line, and a deleted file's are labelled; an empty `--tag` is worded; tags are not read when nothing
   uses them). The help came in slice 6.
5. *The tags CSV*: `TagItems`, `RecordTagsExport`, `UpdateTags`; a row already applied is unchanged (found by the
   CLI test before the review); `internal/tagcsv`; an older retention CSV is not a tags CSV missing its column;
   `filo export tags` and the update. Review: seven findings, all fixed (a row left as exported is unchanged
   whatever happened since; a tag starting with an apostrophe survives the round trip; a file under two exports in
   one CSV is refused; Ctrl-C at the question is an interrupt; a dry run shows the spelling the run would give; and
   `design.md` corrected where it promised `-y` and "no terminal: nothing changes").
6. *Help and README*. Review: six findings, all fixed (a README example's unquoted tag; help gaps for update, find,
   tag and show; the `-y` in `design.md`, as in slice 5).

Tests: tags `TestParse`, `TestParseSearch`, `TestMatches`, `TestFoldAndSame`; walk `TestSkipsPath`; store
`TestMigrateFromV10`, `TestTagFiles`, `TestUntagFiles`, `TestCopies`, `TestFoundAndShownTags`, `TestTagSummary`,
`TestRenameTag`, `TestRenameKeyRespells`, `TestTagItemsAndExport`, `TestUpdateTags`; reconcile `TestScanCopiesTags`,
`TestPossibleMoveNotesTags`, `TestRegisterFile`, `TestPrepareFileRefuses`, `TestPrepareFileHardLinkAndCopy`,
`TestPrepareFileCopyInAnotherRoot`; find `TestSelectTags`, `TestRender`; show `TestRender`; tagcsv
`TestWriteAndParse`, `TestParseCSVRows`, `TestKinds`; CLI `TestTagGolden` (`testdata/tag.txt`), `TestTagRefusals`,
`TestTagUsage`, `TestTagsGolden` (`tags.txt`), `TestFindTags` (`find-tags.txt`), `TestTagsCSVGolden`
(`tags-csv.txt`), `TestTagsCSVKinds`, `TestTagsUpdateInterruptedAtPrompt`, `TestScanCopyTags`, and the help pages.
Run by hand on a throwaway tree: tagging, a copy registered by `filo tag` taking its original's tags, find by tag
(terminal and piped), `filo tags`, `filo show`, and a move that keeps the tags.

## Tag index pages: how item 38 was built

Proposed by the person on 2026-11-21 as HTML pages written by `filo export tags -o DIR`; on 2026-11-22 they replaced
that with indexes declared once on a directory and kept up to date by Filo, so the links are relative and work on any
device that syncs the directory (they tested a relative link on their phone first: it opened the file). Designed with
the person topic by topic the same day: `filo indexes add [DIR] [--name NAME]`, list, `remove` (asks) and `refresh`
(their name for it); `NAME.html` and `.NAME/` skipped by every scan by exact path, not by the root's ignore names;
Filo replaces or removes only its own unchanged files (by hash); the hard rule widened for them; pages written after
every run that commits, the data hashed first (the person's idea, first as XML and XSLT; `html/template` chosen
instead: no dependency, no cgo, escaping by context); a failure exits 1; page names a slug plus a hash; the "no …"
pages told apart by place and wording; rows without size or retention; a local stylesheet for phones. The outputs of
`filo indexes` were mocked with the person before building: a successful update says nothing; `remove` that leaves
files exits 1. Built on branch `tag-index` on 2026-11-22, one slice, reviewed once (`code-review`, medium).

Settled while building, by Claude, and written into `design.md`: a key's page is `.NAME/<key>/index.html` and the
bare tags' are under `.NAME/_/` (a key may start with `_`, so `.NAME/_untagged.html` and a key page could clash); the
pages are written at the end of a run, before its snapshot (`closeStore`), not after chosen commands; dates from a
modification time are UTC, so every device writes the same pages; temporary files are written in `.NAME/`; the
failure line reads "the change is made, but … was not brought up to date: …"; the pages were built before the
commands, so `add` writes real pages from its first commit. A change that does not alter what an index shows writes
nothing, so a page edited by hand is flagged only when its data next changes, or by `refresh`.

Commits, lower layers first: the design moved in; schema 12 (`tag_indexes`, `tag_index_files`), the index paths among
the register's own paths (`OwnPaths` now reads the register); `IndexFiles`; `internal/tagindex`: the data, page names
and hash, rendering (embedded template and stylesheet), writing and removing with the ownership checks, `Update`;
`view.TableRow.Below`; `filo indexes`; the pages following every change; help; README; the hard rule in `CLAUDE.md`
with `TestOnlyWriteTouchesTheDisk`. Review: nine findings, all fixed: a root absorbed by `filo roots add` of a parent
now takes its tag indexes (before, the add failed on the foreign key); `filo indexes` lists an index whose data cannot
be built instead of failing; a directory Filo cannot remove keeps its record unless it holds other files; an unchanged
index is skipped before rendering; temporary files in `.NAME/`; non-ASCII keys get a hashed directory name (two Unicode
forms of one key are one name on APFS and NTFS); add's directory problem worded; remove's closing line as designed;
and `remove` of an index whose root cannot be reached (a drive not mounted, lost or formatted) now says its files
cannot be removed and would be registered should they come back, and asks whether to remove the index anyway (the
person's decision; an index whose directory is gone is removed with nothing more to say).

Tests: store `TestMigrateFromV11`, `TestValidIndexName`, `TestAddTagIndex`, `TestRecordAndRemoveTagIndex`,
`TestOwnPathsHoldIndexes`, `TestIndexFiles`, and `TestAbsorbKeepsHistoryAndReviews` with indexes; reconcile
`TestTagIndexFilesNotRecorded`; tagindex `TestBuild`, `TestHash`, `TestPageName`, `TestKeyDir`, `TestRender`
(`testdata/sample.txt`), `TestRenderEmpty`, `TestRenderEscapes`, `TestWrite`, `TestWriteLeavesWhatIsNotFilos`,
`TestWriteUnreachable`, `TestWriteKeepsADirectoryWithAFiloFileLeft`, `TestTempFilesInThePagesDirectory`,
`TestRemoveAndPlan`, `TestUpdate`, `TestOnlyWriteTouchesTheDisk`; view `TestTableBelow`; CLI `TestIndexesGolden`
(`indexes.txt`), `TestIndexPagesNotScanned`, `TestIndexPagesFollowChanges`, `TestIndexesRefusals`,
`TestIndexRemoveAway` (`indexes-remove-away.txt`), and the help pages. Run by hand on a throwaway tree: adding an
index, a tag updating the pages without a word, a scan finding no new file. Not tested: an index whose data cannot be
built (two tags with one page name, which the hash makes all but impossible to produce); the pages in a real browser
or on a phone.

## Makefile and install script

Added 2026-11-23 on branch `improvements`: a Makefile (`make build`, a static `CGO_ENABLED=0` build; `make install`)
and `scripts/install.sh`, which copies `bin/filo` to `~/.local/bin` on the host. A review the same day found that the
first commit had no `install` target (its message named one) and passed `-X main.version` to a variable `cmd/filo`
does not have, which the linker ignores; the target was added and the stamp moved to roadmap item 22 (`filo
version`). Checked by hand: `make build`, and `scripts/install.sh` with `HOME` set to a throwaway directory, from the
repo and from `/`.

## Export headings and marked file_dates

A friction fix from real use, discussed and built 2026-11-24 on branch `improvements`. The export's heading,
`Retention export   export 18 · 5,222 files`, said "export" twice and did not show that 18 was an ID; it became
`Export #18 (retention)`, with `Scan #17` and `export #18` in update headings to match. The `!` line asked to check
2,766 dates read from file names, but the CSV gave no way to find them, and filling `7y` down a directory accepted
every guess unseen; such a date is now written `!2023-07-14` and counts only once the `!` is removed. Considered and
left out: an info line or per-directory lines in the CSV (they break sort, filter and fill-down), a separate Y/N
"reviewed" column (filling it down checks nothing), and recording in the register where each file_date came from
(the mark keeps unchecked guesses out of it). Links to files from the CSV stay roadmap item 29.

Commits: the design, the update reading marked dates, the export writing them, the README, the headings. The code
review (medium) found a test that could no longer fail (it looked for the old heading), a refusal calling an edited
marked date "read from the file name", "accept one" in the plural `!` line, help text saying a marked date "is not
used" where a period on it is refused, no test of a marked date on a period policy, and `#` only in headings; on
the last, the number became `#N` in every message (refusals, report, show, scan events), and the design and README
now say to format the `file_date` column as text before removing the marks (checked by hand in LibreOffice Calc on 2026-11-24: removing the `!` kept
the date as `YYYY-MM-DD`; Excel not checked). Tests: a
table of the update's cases for marked dates in `internal/store`, marked cells in the CSV parser, the plural `!`
line, golden files, and a run by hand on a throwaway tree.

## The stall after commands

A friction fix from real use, found and built 2026-11-25 on branch `improvements`. Commands stalled after their work
was done. The guess was the tag index pages, with a single page reading JSON proposed instead. Measured on a
throwaway tree of 20,000 tagged files, the stall was a query: the hard-link count in `store.FindFiles` searched
`paths` by root for each file, as SQLite has no statistics to prefer `ix_files_inode`, so it read every current path
of the root once per file (more than 100 s for the query alone). Every command that changes the register ran it once
an index existed, since an index's data is built before its hash says whether the pages need writing; `filo find`
and the tags export ran it too. Writing `+lp.root_id` makes it search files by inode and then their paths: 0.2 s
for the 20,000 files. After the fix, a run that changes what an index shows took 1.4 to 1.8 s on that tree, nearly
all of it rendering 26 MB of pages (mostly the pages of files with none of a key's values); a run that does not
change it, about 0.2 s. The single page was not needed for speed.

Commits: the fix and a test of the query's plan. The code review (medium) found no bug in the query; on the test,
it found that the check read past the subquery's steps (a later outer step could fail a good plan), did not require
the paths step to use `ix_paths_file`, and matched SQLite's exact plan wording; all three were fixed. Tests: the plan
check, for every root, a root and a scope, failing without the `+`; `TestFindFilesLinks` unchanged; and timings by
hand on the throwaway tree.

## Faster tags updates and index pages

A performance review after the stall fix, built 2026-11-25 on branch `improvements`. Profiled on a throwaway tree of
20,000 tagged files with one tag index. Two changes, picked from the review's list; the others (planner statistics
with `PRAGMA optimize`, fewer pages, cheaper snapshots, one query for `FileTags`) were left, the first three needing a
design decision.

- The tags CSV update compiled the same few queries again for each row: a third of its time. `store.stmtCache`
  prepares each distinct query once in the transaction, and a tag's spelling is looked up once per spelling as
  written while the rows are checked. `UpdateTags` on 20,000 rows: 2.7 s to 1.2 s.
- Rendering a changed index was nearly all `html/template`, one page at a time. `tagindex.Render` now makes every
  page and then executes them on every processor, longest first; the pages are the same bytes. `filo indexes
  refresh` (and `filo tag` under the index) with 97 MB of pages: 6.0-7.1 s to 2.4 s on 8 processors. What is left is
  mostly the single largest page (4.3 MB), which renders on one processor.

Commits: the statement cache, the parallel rendering. The code review (medium) found no bug; it found that a failed
page's name in the error had changed, that the longest-first order ignored pages of rows, and that the cache's
comment did not say a failed prepare runs the query again uncached: all three fixed. Not done: stopping early when a
page fails, and a test that the error is the same on every run (a page cannot fail with the embedded template).
Tests: `TestStmtCache`, a case of a spelling repeated over rows in `TestUpdateTags` (a dry run shows each as written),
`TestRenderParallel` (one processor and eight give the same pages), the unchanged golden file, the race detector on
both packages, and timings by hand on the throwaway tree.

## Duplicated code merged

A review for duplicated lines, built 2026-11-25 on branch `improvements`. A scan for runs of six or more identical
lines in the non-test code found little copying; most runs were import blocks, OS-specific twins and a migration's
copy of a table, all left as they are. Three were merged:

- The retention, tags and duplicates CSVs each wrote the byte order mark and header, and a file's directory, name,
  size and modified cells, themselves; the retention and tags updates each read the id and export and checked for
  text past the columns; and three packages had the same `Problem` type. These are now `sheet.NewWriter`,
  `sheet.FileCells`, `sheet.ReadIDs` and `sheet.Problem` (the tags update no longer copies its problems into
  retention's to list them).
- `filo set` and `filo update` each built the "now past its retention" list; both call `nowPast`. The update, from a
  CSV, shows the first ten in a terminal; `filo set`, a command naming its files, shows every one (the user's
  decision, now in `design.md`).
- `store.IndexFiles` repeated `TagItems`' steps; it calls it.

Proposed and not done: one helper for the "all, select, list, none" question that `filo delete` and `filo delete
FILE.csv` each have (the destructive path, left for a separate decision), and smaller ones (the prompter setup,
hand-written row loops).

Commits: `IndexFiles` on `TagItems`, the sheet parts, the CSVs using them, `nowPast`. The code review (medium) found
no bug; two nits (`sheet.Problem`'s comment named only updates; `nowPast`'s parameter `max` hid the built-in) were
fixed. The rename of `max` was made by a text replacement that also changed the same text in the clears list, and
was committed without the checks passing: commit `18a463f` does not build, and the commit after it fixes it. Found
by the review and older than this work: with a root at `/`, the CSVs wrote a directory cell such as `//etc`;
`FileCells` now joins with `filepath.Join` (fixed after the series, at the user's request, with two cases added to
`TestFileCells`). Tests: `TestNewWriter`, `TestFileCells`, `TestReadIDs`; the CSV golden files and the message tests of the
retention and tags updates, unchanged.

## One CSV with every column

A friction fix, built 2026-11-27 on branch `improvements`, the first of three parts agreed that day (then roadmap 39,
renaming by the `name` cell, and 40, removing the retention and tags exports). Editing a retention CSV the person kept
seeing tags to set, and dates while tagging; `filo export [DIR] -o FILE` now writes `file_date`, `retention`,
`keep_until` and `tags` in one CSV, recorded as an export of kind `files` (no schema change). Decided with the person:
`-y` means the same for clearing a retention, removing a tag and creating a policy, and the update shows them all
before it writes anything and asks once; no records nothing. Decided while building, and checked with the person
afterwards: a row refused by either half is refused whole; a CSV with both columns is combined only if its rows name a
combined export (so a retention CSV with a `tags` column of the person's own stays one); a combined CSV that lost a
column is refused as a whole; a symlink's row cannot take tags.

Commits, lower layers first: the roadmap items, the design (and the symlink rule), `FilesItems` and
`RecordFilesExport`, a refactor giving `checkRow` and `checkTagRow` the export kind and moving the writes into
`createPolicies`, `applyRetentions` and `applyTagChanges`, `UpdateFiles`, package `filecsv` (with `retention.ReadRow`
and `tagcsv.GuardCell` exported for it), `ExportKinds`, and the CLI. The code review (medium) found one important
issue, fixed: a combined CSV whose rows were all refused for their cells was read as a retention CSV, with the wrong
reasons; `filecsv.Parsed.Exports` now names every export a row names. Nits fixed: a file listed twice, one copy
refused for its cells, had the other applied; a lost `tags` column given `-y` got the `-y` usage error; that error
was out of date; messages named the kind `files` (now "combined"); a question that could not be shown read as "not
confirmed"; the `./tags` wording. One nit was the person's decision: with no terminal and no `-y`, nothing recorded
now exits 1 (a no typed at the question stays 0). Tests: `TestFilesItemsAndExport`, `TestUpdateFiles`,
`TestExportKinds`, the `filecsv` tests, `TestFilesCSVGolden` (`files-csv.txt`), `TestFilesCSVKinds`,
`TestFilesUpdateInterruptedAtPrompt`, and the help golden files. Checked by hand on a throwaway tree: export, update
without a question, the no-terminal refusal, a dry run and `-y`.

## Renaming by the name cell

Roadmap 39, built 2026-11-27 on branch `improvements`, the second of the three parts agreed that day. A changed
`name` cell in the combined CSV renames the file in its own directory. The design was settled in seven topics: the
hard rule (one package, `internal/dispose`, now changes the user's files in two ways, deleting from `filo delete` and
renaming in place from `filo update`); the check (the very file the last scan saw, no re-hash); the names allowed
(the person chose to allow a change of spaces at the start or end, and to refuse hiding or unhiding, not every
leading dot); recording (a scan of kind `rename`, a `moved` event, schema 13 with `export_rows.name`); the order
(the register's changes first, then each rename, recorded straight after; a rename not made keeps its row's other
changes); the output (old and new names side by side, quoted when a space starts or ends one); edge cases (hard
links, symlinks, Syncthing, no `renameat2` off Linux). The Direction's "standard tools" rule was clarified by the
person: it rules out clones of file commands, not changes through the export and bulk update.

Added while building, each with its own design commit: a name cell doubles a leading apostrophe, as the tags cell
does; a combined export row keeps the path it showed (`path_id`); in a chain or swap both rows are refused.

Commits: the design; schema 13; `dispose.CheckName`; `sheet.Row.Raw` (a cell with its spaces); the `filecsv` name
cell; `dispose.Rename`; `store.RenameRun` and the exported path; the update's rename checks; `dispose.CheckRename`;
the view's `Renames`; a store fix naming a refused row's file; the CLI. Commit `26b0b0a` was made with a test
failing (its output was piped through `grep`, which hid the exit code); `9ce2e19` fixes the test, with no code
change, and every commit after was checked on the test's own exit code.

The code review (medium) found three important issues, all fixed: a Ctrl-C landing between a rename and its record
could leave the file renamed on disk but not in the register (the record now ignores cancellation); a rename could
give a file the path of one of Filo's own files (a tag index's pages, the database), which no scan records (now
refused, from `OwnPaths`); a register write failing part-way hid what the run had done (now shown, then the error).
Nits fixed: a rename that a scan cannot put right is listed under "Not renamed", and on a system without
`renameat2` every rename is refused before the question (that change went into commit `e5311cb`, whose message names
only the Ctrl-C fix); an untouched name cell of an export from before schema 13 no longer refuses its row; a name
starting with `'` is made valid UTF-8; no "0 of 0 renames" after Ctrl-C; a hidden name's dot is not an extension.
One was the person's decision: a rename of a name a spreadsheet may have reformatted (`0012`, `1E5`, `1-2`) is
noted in the summary, not refused. Also at the person's request: `TestOnlyDisposeChangesUserFiles`, which parses
every Go file of the module and fails on a call that removes or renames on the disk outside `internal/dispose` and
the three files that handle only Filo's own files (checked by planting such calls). Until then no test guarded the
rule that only `internal/dispose` deletes; an earlier message in the design talk had said one did.

Tests: `TestCheckName`, `TestRename`, `TestRenameNeverReplaces`, `TestRenameSymlink`, `TestCheckRename`,
`TestRenameRecordedAfterCtrlC`, `TestRowRaw`, `TestNameCell`, `TestNameColumn`, `TestMigrateFromV12`,
`TestRenameRun`, `TestFilesExportRecordsPath`, `TestUpdateFilesRenames`, `TestRenames`, `TestRenameRowNotes`,
`TestRenameGolden` (`rename.txt`), `TestRenameNoTerminal`, `TestOnlyDisposeChangesUserFiles`. Checked by hand on a
throwaway tree: two renames from the CSV (one only removing a trailing space), a dry run, `-y`, and a scan after
finding no changes.

## No special case for bin and obj

Roadmap 41, built 2026-11-27 on branch `improvements`, agreed while designing 42 (moving by the `directory` cell),
whose check of what the next scan sees would otherwise have had to repeat it. Until then a scan skipped `bin` and
`obj` in a directory holding a `.csproj` or `.fsproj`: a hard-coded case, which the person judged "a can of worms".
Now they are scanned like any directory. **On upgrading**, the next scan of a root holding a .NET project registers
everything its build directories hold, as new files needing a retention (and Debug and Release copies of one output
as duplicates); adding `--ignore bin --ignore obj` to the root's settings, with its other settings given again since
`filo roots set` replaces them all, keeps them out.

Commits: the roadmap item, the design, the walk (`skipName` and `SkipsPath` lose the case; `SkipsPath` its root
parameter, used only to look for project files), the README. The test tree's `bin/out.dll` is now scanned, so its
first scan finds 16 files. The code review (medium) found one important issue, fixed: the README's `--ignore` hint,
typed as given, replaced the root's other settings (`roots set` replaces them all). Nits fixed: an ignored name skips
files too; the README's sample scan count; the replaced decision moved here from `design.md`. Tests:
`TestSkipsPath`, the walk table's "bin and obj beside a project file are directories like any other",
`TestTestTree` (walk, reconcile) and `TestEndToEnd` with the new count.

## Moving by the directory cell

Roadmap 42, built 2026-11-27 on branch `improvements`, after 41. A changed `directory` cell in the combined CSV moves
the file within its root, making the directories it needs. Designed in seven topics with the person: within the
file's own root only; directories made as needed, listed as new first (the person chose this over existing
directories only); what the next scan sees (the scan's own rule, which led to 41: the person asked that `bin`/`obj`
be dropped from the scan rather than repeated); nothing re-done for directory rules (a retention of its own overrides
any directory's, the person added); emptied directories removed as `filo delete` does (the person's decision, over
leaving them: an emptied `inbox/` goes too); absolute cells only; edge cases (relative symlinks refused, breaking
symlinks noted, Syncthing noted for 9a).

Commits: the design; the store's move checks (behind an option, so no commit in between moved anything); `dispose.
CheckPath`; the `Mover` (a rename is now a move within one directory; `pruneDirs` shared with `filo delete`); the guard
test extended to making directories, each of Filo's own files allowed only the calls it needs; `filecsv` reading the
directory cell; the view's `Moves`; the symlinks a move breaks; the CLI. No schema change: the directory cell as
exported is worked out again from the path the export recorded.

Found while building: the first end-to-end run hung. The question is asked inside the update's transaction, and the
summary read the symlinks through the store, which has one connection; they are now read inside the transaction.

The code review (medium) found two important issues, both fixed: a combined export that recorded no path had every
row refused (its directory cell is now compared with the file's directory now); and the path checks judged the
directories and name the file already had, so a plain rename under an oddly named directory was refused (only what
changes is judged now). Nits fixed: on a filesystem that ignores case, a directory spelled otherwise than on disk is
refused (not tested: the dev container's filesystems are case-sensitive); a move into Filo's own directory names the
directory; a file on another filesystem is refused before the question; a directory changed only by spaces is noted;
the unused `Rename` and `CheckRename` became test helpers. One was older than this work: a rename breaks symlinks too,
and now says so.

Tests: `TestCheckPath`, `TestMove`, `TestMoveUnmakesDirs`, `TestMovePrunes`, `TestOnlyDisposeChangesUserFiles` (now
with directories made; checked by planting calls), `TestDirInRoot`, `TestUpdateFilesMoves`, `TestMoves`,
`TestMoveRowNotes`, `TestMoveGolden` (`move.txt`), and the rename tests, now through the `Mover`. Checked by hand on a
throwaway tree: three files moved (one renamed too), four directories made, the emptied `notes/` removed, and the next
scan found no changes.

## Removing the retention and tags exports

Roadmap 40, built 2026-11-27 on branch `improvements`, after 42, once the person had used the combined CSV ("filo
export works brilliantly"). `filo export retention` and `filo export tags` and their updates went, leaving `filo
export [DIR] -o FILE` and `filo export duplicates`. The open question, whether `filo update` kept reading an old
retention or tags CSV for a while, was settled by the person: refuse them. At first with a pointer to `filo export`;
then, since only the person had used them, with no hint at all: an old CSV is "not a filo CSV", and `retention` and
`tags` after `export` are directory names like any other (only `./duplicates` needs the `./`). `-y` now means the
same for every update.

Commits: the design (Lifespan and Retention CSV columns now describe the combined CSV's retention half; the tags
CSV's cell rules moved under "The combined CSV"); `filecsv.LostColumn`, which tells a combined CSV that lost a column
without the old readers; the CLI; the README, which had not yet described the combined CSV, renames or moves; the old
CSVs' readers and writers in `retention`, `tagcsv` and `filecsv` (the retention reader's row tests moved to
`filecsv.ParseCSV`); the store's own functions for the old exports (`RecordExport`, `UpdateRetention(With)`,
`RecordTagsExport`, `UpdateTags`, the `retention` and `tags` kind constants), its tests moved to `UpdateFiles`
through two helpers; the last mentions (the `;` refusal now says it separates tags in a CSV). No schema change:
older registers keep exports of the old kinds, whose rows the combined update refuses by kind.

The code review (medium) found six nits. Fixed: Named retention policies still described the old update's own
policy question; a `group` column of the person's own hid a lost column; a row typed past the last column did not
count its export, so a lost column was reported as "not a filo CSV"; the overview help for `update` left out `-y`,
names and directories; a test comment said the register was not opened before an old CSV is refused (it is read, to
tell it from a combined CSV that lost a column). Not fixed, by the person's decision: `export retention` is read as a
directory, with no pointer.

Tests: `TestLostColumn`, `TestParseCSVRetentionHalf`, `TestParseCSVLimits`, `TestGuardCell`, `TestParseCell`,
`TestUpdateNotAFilesCSV`, `TestFilesCSVKinds` (both columns lost; `./duplicates`; a directory named `tags`),
`TestUpdateWithGroupColumn`, `TestTagItems`, `TestUpdateTags` and `TestUpdatePolicies` (now on the combined update:
a declined question records nothing), the round trips and goldens through `filo export` (`update.txt`,
`update-dry.txt` and `update-clear.txt`, renamed from `update-retention*`; `tags-csv.txt` removed). Checked by hand on
a throwaway tree: `export retention` exports a directory named `retention`, an old retention CSV is not a filo CSV.

## The tag index's find page

Roadmap 44, designed with the person and built on 2026-11-27 on branch `improvements` (the person: one feature
branch and one PR at the end of the week), right after 40. With about 100 tags, browsing one tag at a time was the
friction. Designed topic by topic: matching (either value within a key, keys and bare tags all, counts that narrow);
placement (the person first asked for one page at `~` with directories as a filter, maybe no indexes; since every
device opens the index from the synced `~/Files`, indexes stay as they are and each page gets a directory filter;
the person keeps one at `~` and one at `~/Files`; offering an index at the top of a synced directory is noted for
9a); the tag picker (one box suggesting tags, collapsed keys, chips); everything inline in `NAME.html` (the person
had checked a script and an external stylesheet run on all five devices); untrusted names (JSON, `textContent`,
links from Go, a Content-Security-Policy with no network); a preview pane (the person's idea); one machine keeps a
synced index until 9c (Filo runs on the laptop only for now). The device check with a test page was skipped.

Commits: the design; Node 26 LTS in the Containerfile (the person agreed to the dependency, for tests only; the image
build itself was not run here, only its download, checksum and extraction); the script's logic with `node --test`
tests run from `TestFindScript`; the find page (the list of keys moved to `.NAME/index.html`, a "Find files" link
on every static page, `layout` 2); a gofmt fix for the previous commit, which was committed although `gofmt -l`
flagged it (the check chain now stops before committing); the preview pane; README and help.

The code review (medium) found three important issues. Fixed: tags were suggested only among files whose names held
the typed text, so typing a tag's name usually suggested nothing (the page test had typed a word that was in a file
name); "Show more" never hid (a rule's `display` beat the `hidden` attribute); Chrome blocks its PDF viewer in a
sandboxed frame, so PDFs now preview in an embed declared `application/pdf` (the person's choice; design.md
changed). Nits fixed: the header lost its tag count; a tag missing from the index's list would have been shown as
another tag (now an error); a stale comment; a test helper that could panic; HEIC pictures outside Safari (the pane
switches to "No preview" on a load error, the person's choice). Fixing the policy test also showed it had passed
only by luck: `html/template` writes a `+` in an attribute as `&#43;`, and the test now reads the attribute as a
browser does.

Tests: `find_test.js` (matching, facets, suggestions, subdirectories, the address, the kind of preview, and the
drawing code on a fake DOM), `TestFindScript`, `TestRenderFindPage` (the policy against what is inlined, the data),
`TestRenderEscapes` (hostile names in the data, a tag named `</script>`), the goldens. Checked on a throwaway tree:
the page's data parses and its script's hash is in the policy. Not checked in a browser or on the devices yet: how
the page looks and behaves there, PDF previews on each device.

**The PDF preview on the iPad** (2026-11-27). The person found the page worked on the desktop and laptop, but on the
iPad, in Textastic, the preview was white and Open did nothing. Checked with throwaway pages copied to the iPad (not
committed), one question at a time. Textastic serves the page from its own local web server
(`http://localhost:8080/External/<id>/…`); once it was opened from there, Open, pictures and text files all worked
with the policy unchanged. Open doing nothing was most likely a one-off: the first page checked once loaded as
`about:blank` with no address, so no relative link could work, and never did again. What stayed broken was the PDF:
the console showed `connect-src` blocking it, since Safari's PDF viewer fetches the embedded file. With `connect-src
'self'` an `embed` (and an `object`) showed both pages of a two-page test PDF; a frame, sandboxed or not, showed only
the first. The person agreed to the change: the policy was there so the page never loads scripts or stylesheets
from the internet and works offline, not to stop it reaching the server that serves it. Replaces "no network" above
with "nothing from any other origin".

Commits: the design; the policy; one review (medium), no bugs, five nits fixed: the design overclaimed ("cannot send
a name anywhere"), asserted untested what `'self'` matches on a `file:` page, and held dated device notes (now here);
a test that the script has no call that could fetch (it fails on a planted `fetch`); and this note. Tests:
`TestRenderFindPage` (the policy, no fetching call), the goldens. Not checked yet: a PDF in the real find page on the
iPad, and the other devices since the change.

## Security review of 2026-11-27

A security review of the whole code, asked for by the person on 2026-11-27, looking for exploits, each tried on a
throwaway tree. Built on 2026-11-27/28 on branch `improvements`, straight from the review (no roadmap items: the
person chose to do them at once). Found sound: disposal through the opened directories, `view.Escape`, the pages'
`html/template` and Content-Security-Policy, parameterised SQL, `O_EXCL` output files and the private register.

- **A tag index written through its opened directories** (`7fdfdf5` design, `13e3f99`, `8197278` review fixes).
  `internal/tagindex` looked with `os.Lstat` and then acted by path, so a `.NAME/` swapped for a symlink between the
  two was followed. Reproduced: a refresh of 300 changed pages, racing a loop that swapped `.index` with a symlink
  (`mv -T --exchange`), left three temporary page files in a directory outside the index and 170 pages unwritten.
  Now every change goes through `os.Root`s opened from the index's directory down, each a real directory and the one
  lstat saw, opened as soon as it is made or checked; the same race left nothing outside. Tests:
  `TestWriteNeverFollowsASwappedDirectory` (a hook swaps `.index`, then `.index/a`, before the first write),
  `TestWriteForgetsFilesOfADirectoryNoLongerFilos`, and `TestOnlyWriteTouchesTheDisk` now refusing path-based changes
  in `write.go` too (it fails on the old code).
- **No renaming or moving of hidden files by the CSV** (`37d1a3c` design, `1ee9576`, `0562ae5` and `1875100` review
  fixes). The rule was that a rename or move never hides or unhides a file, so in a root with `--include-hidden` a
  move between hidden directories passed. Reproduced with an edited CSV: `.ssh/id_ed25519` moved into
  `Sync/.stversions/` (and the emptied `.ssh` removed), and `Sync/.peer/authorized_keys`, as another Syncthing device
  could leave it, moved into a new `.ssh/`. Now a file with a hidden part on its path is never renamed or moved, nor
  given a hidden path; its retention and tags still apply. This replaced the "hidden to hidden is renamed" row of the
  rename table. Test: `TestUpdateNeverMovesHiddenFiles` (both moves, the rename of `inbox/.notes`, and the key's
  retention still set).
- **Moves leave the directories they empty** (`491ce8e` design, `410747b`). Not a security fix: raised by the person
  in the same discussion, since clearing an inbox through the CSV removed the inbox. Replaces their decision recorded
  with the moves (roadmap 42), "an emptied `inbox/` goes too". `filo delete` still removes what its run empties; a
  hidden `.keep` keeps a directory a delete would empty. Test: `TestMoveLeavesDirectories`.
- **Not acted on.** The formula guard looks only at a cell's first character, so a name such as `report;=1+1;x.txt`
  goes into the CSV unquoted and a spreadsheet splitting at `;` would make `=1+1` a cell. The person's LibreOffice
  splits at commas (a tags cell, `;`-separated, stays whole) and leaves "Evaluate formulas" off, so nothing was
  changed; excluding such names from the export, and listing them, was the fix kept in reserve. The find page's PDF
  preview is an `embed` with no sandbox: a server that sniffs content could show a `.pdf` holding HTML as a page of
  the same origin. The servers used (Textastic's, files opened locally) go by the extension.

One review (medium), six nits, five fixed: `NAME.html`'s rename went by path through `.index` with no check that it
was still the directory opened, and a swap showed `os.Root`'s "path escapes from parent" (now checked before and
after the rename, reported in Filo's words); a page in a directory that is no longer Filo's stayed recorded and was
reported on every run; a design clause and `extension()`'s case for a hidden name, both dead once hidden files are not
renamed; a garbled README sentence. Not fixed: every opened key directory stays open until the end of the run, one
descriptor each; an index has a few keys, and Go raises the soft limit on open files to the hard one.

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

- `filo show`, `set` and `get` (2026-11-20/21) replaced roadmap "Later b, setting dates in place", whose open question
  (a command, the UI, or both) was answered for the command line. A bare `filo FILE` was considered and not taken: a
  file named like a command would be guessed at.
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
