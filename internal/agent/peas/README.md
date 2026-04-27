Place cached PEASS-ng scripts in this directory before building agents that need offline PEAS support.

Run:

    make update-peas

The updater writes `linpeas.sh` and `winPEAS.bat` here. Those files are embedded into subsequently built agent binaries, and are intentionally ignored by git because they are third-party generated payloads.
