Output out/07-help-catalog.mp4
Source tapes/settings.tape
Source tapes/setup.tape

# Curated, API-tested examples in --help ...
Type "dci anomalies-recent --help | head -30"
Sleep 800ms
Enter
Wait+Screen /Examples/
Wait
Sleep 5s

# ... and in the machine-readable catalog.
Type "clear"
Enter
Type `dci commands --json | jq '.commands[] | select(.path==["anomalies-recent"]) | .examples'`
Sleep 800ms
Enter
Wait+Screen /description/
Wait
Sleep 3s
