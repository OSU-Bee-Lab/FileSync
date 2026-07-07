# Data Storage Schema

FileSync syncs audio-experiment data organized under a fixed schema used by the
Johnson lab. This is a summary; the canonical spec lives in the
[schema doc](https://docs.google.com/document/d/1q9SS36vbQIg6BZ3AaXpb2KvZyMGUJfuOGdGQHJLggSk/edit?tab=t.0).

## Structure

```
[location]
└── experiments/
    └── [experiment directory]  "researcher name - experiment name"/
        ├── metadata.csv          (required)
        ├── README.txt            (required)
        └── [optional intermediate directories]  any name/
            └── [recorder directory]  recorder ID/
                ├── 230802_0751.mp3   (audio file, device-native name)
                └── ...
```

## Rules

- **Experiment directory** — named `researcher name - experiment name` (e.g.
  `Luke Hearon - Golden Forage`). One directory per experiment (roughly, one
  manuscript's worth of data).
- **metadata.csv** — required. Connects recorder IDs to experimental factors
  (e.g. columns: recorder, site, date_deployed, treatment). No fixed columns.
- **README.txt** — required. Human-readable description of the experiment,
  methods, and how to interpret the metadata / directory structure.
- **Recorder directories** — named *only* by the recorder ID. The directory
  name **is data**: audio files carry no device info, so files must never be
  moved out of their recorder directory.
- **Audio files** — kept in the device-native format `yymmdd_hhmm.mp3` (e.g.
  `230903_1302.mp3` = 2023-09-03 13:02). Never renamed.
- **Intermediate directories** — everything between the experiment directory and
  recorder directory is free-form (commonly by deployment date or site). Sorting
  which recorder was where is metadata.csv's job, not the directory tree's.

## Locations

Files are mirrored across three synced locations; **Lab Server is canonical**.

- **Lab Server** (canonical) — `/media/server storage/experiments`
- **Teams** (SharePoint) — `/audio_bee_detection/experiments`
- **External** (Seagate) — `/media/Expansion/audio_bee_detection/experiments`

## Warning

Never rename or move files/folders in one location without doing so in all
three simultaneously. Divergence causes duplicate audio, regenerated
buzzdetect results, and painful reconciliation.
