# 

# Various
For a OneDrive remote setup/edit, show only the name and site URL; the rest can go under advanced

## Sync selection



## Sync preview screen
Use buttons for from/to? Idk we want to overall overhaul the UI here. It should be a simple 



# Preferences
Configure checkers
Filtering?


# Remove all backwards compat
We don't have users yet! Decruft.


# Experiemnts listing page (from/to)
Gray out preview until an experiment has been selected


add option for intermediate dirs in recorder sync - display the ident of the sync location



add a setup recorders feature
- translate the Olympus logic and incorporate
- figure out how to edit the capabilities xml on Sony ICD-PX370; check if that worked


for ICD-PX370, sync all audio files found?


Add a configure recorder button? I'm not sure, maybe it has to be hardcoded.


### Recorders
Each recorder should have its own .go file under recorders/ (that dir can go wherever it makes sense for a go project, I don't know how go does architecture). The file defines (i) how to handle setup logic (creating recorder ID), (ii) how to detect the recorder, (iii) how to copy files, including respecting recorder ID


# Pull Files
Completely rework to use the same screen as the sync screen; user chooses a location, then an experiment (or multiple), then the app does a scan first, then users can click to highlight the files/folders (when clicking a folder, all files within are selected)


# Recorder Sync
Split into two cols (or dynamically re-col?)

