# Wiki source

These `.md` files are the source for the project's GitHub Wiki. The wiki itself lives in the separate `*.wiki.git` repo that GitHub provisions per repository.

Publishing is automated by [`.github/workflows/wiki.yml`](../.github/workflows/wiki.yml): on every push to `trunk` that touches `wiki/**`, the workflow clones the wiki repo, copies every `wiki/*.md` file into it — **except this `README.md`** — commits, and pushes.

That means:

* Edit pages here, in this `wiki/` directory; commit and push to `trunk`. The wiki updates itself.
* `Home.md` is the wiki landing page.
* Other file names become wiki page titles (e.g. `CLI.md` → "CLI"). Use kebab- or PascalCase, since GitHub Wiki page names go directly into URLs.
* Inter-page links use bare page names: `[CLI](CLI)`, not `[CLI](CLI.md)`.
* Don't add a `README.md` to the wiki; this file is intentionally excluded so it can document the publishing flow without showing up as a wiki page.

To manually trigger a publish, run the **Publish Wiki** workflow from the Actions tab.
