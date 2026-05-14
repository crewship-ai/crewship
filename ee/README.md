# Enterprise add-ons

This directory holds source for commercial Crewship features that are
distributed under a separate license from the rest of the project.

## License

Core Crewship is **Apache-2.0** (see [LICENSE](../LICENSE) in the repo
root). Code under `ee/` is licensed separately and is **not** covered by
the Apache-2.0 grant. The terms for each module live alongside the
module itself; absence of a LICENSE file means the module is not yet
publicly distributed.

## Status

Currently empty. The directory exists so the dual-license boundary is
explicit in the repo layout from day one — code that ships here in the
future does not require restructuring the tree or rewriting the
top-level LICENSE.

If you are evaluating Crewship for production use today, you only need
the core Apache-2.0 distribution. No feature in shipped releases
depends on anything under `ee/`.

## Contributing

Contributions to `ee/` are accepted only via signed Contributor License
Agreement (CLA). Open an issue first to discuss before opening a PR
that targets this directory.
