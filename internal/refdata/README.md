# Reference data

No list is bundled. `Load` (run by `db.Migrate`) records version `none` for both and leaves
`ref_smdg_liners` and `ref_locodes` empty, until a licensed file is added.

## Why nothing is bundled

SMDG Liner Code List (https://smdg.org/documents/smdg-code-lists/smdg-liner-code-list/, release v20261028,
xlsx). The legal notice https://smdg.org/legal-notice/ says: "This website, its contents, and documents originated
by SMDG are protected by German Copyright Law. They may be copied and downloaded for free. Free-of-charge
transfer to third parties is permitted. Commercial use requires written permission by SMDG e.V."
Ask linercodes@smdg.org for written permission before adding the file.

UN/LOCODE (UNECE, https://unlocode.unece.org/publications). The terms https://unlocode.unece.org/terms allow
download and copy "for the User's personal, non-commercial use, without any right to resell or redistribute
them or to compile or create derivative works therefrom", "subject to ... more specific restrictions". The
site footer says "All UN/CEFACT standards are free to use under CC By 4.0 license"; it is not clear that this
covers the code list. The GitHub mirror datasets/un-locode calls the data licence "unclear" and advises asking
UNECE for written confirmation before redistribution or commercial use. Get that confirmation from UNECE first.

## Adding a file

1. Put the file in `data/` (UTF-8, comma separated, quoted if needed). The first line is a header and is skipped.
2. Set `SMDGVersion` / `LocodeVersion` in `refdata.go` to the release name (for example `20261028`).
   Load replaces the table when the stored version differs, and does nothing when equal.

| File | Columns | Stored as |
|---|---|---|
| `data/smdg_liners.csv` | `code,name` | `ref_smdg_liners(code, name)` |
| `data/unlocode.csv` | `country,location,name,function` | `ref_locodes(code = country+location, country, name, function)` |

UN/LOCODE files from UNECE are often Latin-1/Windows-1252: convert to UTF-8 first. SMDG ships Excel only:
export the `code,name` columns to CSV. Keep download archives out of the repo.

`testdata/` holds small hand-written fixtures used by the tests only.
