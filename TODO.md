# Olympus
Are we renaming via timestamps?

# Tag Recorders
Add recorder setup screen with option for Olympus or Sony recorders
- Tag recorders? ID Recorders? Setup Recorder IDs? Assign IDs? Oooh maybe that one



add a setup recorders feature
- translate the Olympus logic and incorporate
- figure out how to edit the capabilities xml on Sony ICD-PX370; check if that worked


# Various
For a OneDrive remote setup/edit, show only the name and site URL; the rest can go under advanced


# Preferences
Configure checkers
Filtering?
Timeout



# Remove all backwards compat
We don't have users yet! Decruft.


# Experiemnts listing page (from/to)
Gray out preview until an experiment has been selected

# Misc
for ICD-PX370, sync all audio files found?
- would let us move away from strict ID, but would make things harder...maybe just leave as-is

### Recorders
Each recorder should have its own .go file under `recorders/` (that dir can go wherever it makes sense for a go project, I don't know how go does architecture). The file defines (i) how to handle setup logic (creating recorder ID), (ii) how to detect the recorder, (iii) how to copy files, including respecting recorder ID, (iv) how to set up new recorders, assigning an ID.


# Pull Files
Completely rework to use the same screen as the sync screen; user chooses a location, then an experiment (or multiple), then the app does a scan first, then users can click to highlight the files/folders (when clicking a folder, all files within are selected)


