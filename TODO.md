# Various

For a OneDrive remote, show only the name and site URL; the rest can go under advanced

## Sync selection



## Sync preview screen
Use buttons for from/to? Idk we want to overall overhaul the UI here. It should be a simple 



# Preferences
Configure checkers
Filtering?



# Experiemtns listing page (from/to)
Only need to show experiments existing on From, we can ignore To (previously I wanted to have some sort of preview of which experients exist where, but I don't think it's really valubale)

Gray out preview until an experiment has been selected


add option for intermediate dirs in recorder sync - display the ident of the sync location



add a setup recorders feature
- translate the Olympus logic and incorporate
- figure out how to edit the capabilities xml on Sony ICD-PX370; check if that worked


for ICD-PX370, sync all audio files found?


Add a configure recorder button? I'm not sure, maybe it has to be hardcoded.


### Recorders
Each recorder should have its own file under recorders/ (that dir can go wherever it makes sense for a go project, I don't know how go does architecture). The file defines (i) how to handle setup logic (creating recorder ID), (ii) how to detect the recorder, (iii) how to copy files, including respecting recorder ID
