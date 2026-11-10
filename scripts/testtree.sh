#!/usr/bin/env bash
# Builds and mutates a throwaway tree for testing filo. Never touches anything outside DIR.
#
#   testtree.sh init   [DIR]   create the tree        (default DIR: ~/FiloTest)
#   testtree.sh mutate [DIR]   apply a known set of changes
#
# Suggested run:
#   testtree.sh init
#   filo scan --root ~/FiloTest          # first scan: everything is new, bin/ and .hiddendir are skipped
#   testtree.sh mutate
#   filo scan --root ~/FiloTest          # expect the results listed under "mutate" below
#   filo report --root ~/FiloTest
set -euo pipefail

cmd="${1:-}"
dir="${2:-$HOME/FiloTest}"

case "$cmd" in
  init)
    if [ -e "$dir" ]; then echo "refusing: $dir already exists" >&2; exit 1; fi
    mkdir -p "$dir"/{docs,photos,notes,projects/app/src,projects/app/bin,.hiddendir}

    printf 'annual report 2024\n'      > "$dir/docs/report-2024.txt"
    printf 'contract terms\n'          > "$dir/docs/contract.txt"
    printf 'tyre receipt\n'            > "$dir/docs/receipt-tyre.txt"
    printf '# ideas\n'                 > "$dir/notes/ideas.md"
    printf '# todo\n'                  > "$dir/notes/todo.md"

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
    echo "mutated $dir"
    echo "expected: moved 2, changed 2 (todo.md, link-to-report), missing 2, new 3, possible move 1"
    ;;

  *)
    echo "usage: $0 init|mutate [DIR]" >&2
    exit 2
    ;;
esac
