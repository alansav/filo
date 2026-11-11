#!/usr/bin/env bash
# Builds and mutates a throwaway tree for testing filo. Never touches anything outside DIR.
#
#   testtree.sh init   [DIR]   create the tree        (default DIR: ~/FiloTest)
#   testtree.sh mutate [DIR]   apply a known set of changes, including making a directory and a file unreadable
#   testtree.sh unlock [DIR]   make them readable again (needed before rm -rf DIR as a normal user)
#
# Suggested run, always with a throwaway database:
#   testtree.sh init /tmp/FiloTest
#   filo --db /tmp/f.db roots add /tmp/FiloTest
#   filo --db /tmp/f.db scan      # first scan: 15 new; bin/ and .hiddendir are skipped
#   testtree.sh mutate /tmp/FiloTest
#   filo --db /tmp/f.db scan      # expect the results printed by mutate
#   filo --db /tmp/f.db report    # docs/private.txt is listed under "Unreadable files"
#   testtree.sh unlock /tmp/FiloTest
#   filo --db /tmp/f.db scan      # nothing new, changed or missing; the unreadable flag is cleared
#
# Permissions are not enforced for root, so run it as a normal user: as root, mutate reports errors 0.
set -euo pipefail

cmd="${1:-}"
dir="${2:-$HOME/FiloTest}"

case "$cmd" in
  init)
    if [ -e "$dir" ]; then echo "refusing: $dir already exists" >&2; exit 1; fi
    mkdir -p "$dir"/{docs,photos,notes,locked,projects/app/src,projects/app/bin,.hiddendir}

    printf 'annual report 2024\n'      > "$dir/docs/report-2024.txt"
    printf 'contract terms\n'          > "$dir/docs/contract.txt"
    printf 'tyre receipt\n'            > "$dir/docs/receipt-tyre.txt"
    printf '# ideas\n'                 > "$dir/notes/ideas.md"
    printf '# todo\n'                  > "$dir/notes/todo.md"
    printf 'private\n'                 > "$dir/docs/private.txt"      # made unreadable by mutate
    printf 'kept\n'                    > "$dir/locked/kept.txt"       # its directory made unreadable by mutate

    head -c 100000 /dev/urandom        > "$dir/photos/img1.bin"
    head -c 120000 /dev/urandom        > "$dir/photos/img2.bin"
    cp "$dir/photos/img1.bin" "$dir/photos/img1-copy.bin"        # a duplicate pair

    printf '<Project/>\n'              > "$dir/projects/app/app.csproj"
    printf 'class A {}\n'              > "$dir/projects/app/src/main.cs"
    printf 'binary\n'                  > "$dir/projects/app/bin/out.dll"   # skipped: bin beside a .csproj

    printf 'hidden\n'                  > "$dir/.hiddendir/secret.txt"      # skipped unless --include-hidden
    : > "$dir/empty.txt"

    ln -s docs/report-2024.txt "$dir/link-to-report"
    ln -s nowhere "$dir/broken-link"
    echo "created $dir"
    ;;

  mutate)
    [ -d "$dir" ] || { echo "no such directory: $dir" >&2; exit 1; }
    mkdir -p "$dir/archive"

    mv "$dir/docs/contract.txt" "$dir/notes/contract-final.txt"   # moved (and renamed)
    mv "$dir/photos/img2.bin" "$dir/photos/img2-renamed.bin"      # moved (renamed)
    printf 'more\n' >> "$dir/notes/todo.md"                       # changed
    rm "$dir/docs/receipt-tyre.txt"                               # missing
    cp "$dir/photos/img1.bin" "$dir/photos/img1-copy2.bin"        # new (a third copy of img1)
    printf 'brand new\n' > "$dir/notes/new-note.md"               # new
    touch "$dir/notes/ideas.md"                                   # touched: counted as unchanged

    printf 'edited\n' >> "$dir/docs/report-2024.txt"
    mv "$dir/docs/report-2024.txt" "$dir/archive/report-2024.txt" # missing + new, same name: "possible move"
                                                                  # also leaves link-to-report broken: "changed"
    chmod 000 "$dir/locked"            # unreadable directory: an error, and locked/kept.txt is NOT missing
    chmod 000 "$dir/docs/private.txt"  # unreadable file, nothing else changed: noticed through its ctime,
                                       # an error, and flagged unreadable
    echo "mutated $dir"
    echo "expected: moved 2, changed 2 (todo.md, link-to-report), missing 2, new 3, possible move 1," \
         "errors 2 (locked, docs/private.txt)"
    ;;

  unlock)
    [ -d "$dir" ] || { echo "no such directory: $dir" >&2; exit 1; }
    chmod 755 "$dir/locked"
    chmod 644 "$dir/docs/private.txt"
    echo "unlocked $dir"
    ;;

  *)
    echo "usage: $0 init|mutate|unlock [DIR]" >&2
    exit 2
    ;;
esac
