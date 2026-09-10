Output out/08-datahub.mp4
Source tapes/settings.tape
Source tapes/setup.tape

# record.sh creates the cli-demo-import dataset before this scene.
Type 'dci export-datahub-dataset-records "Daily Tenant Usage" --all --output-file records.csv'
Sleep 800ms
Enter
Wait+Screen /wrote/
Wait
Sleep 2s
Type "head -3 records.csv"
Enter
Wait
Sleep 3s

Type "clear"
Enter
Type 'dci export-datahub-dataset-records "Daily Tenant Usage" --all --for-reimport --output-file reimport.csv'
Sleep 800ms
Enter
Wait+Screen /wrote/
Wait
Sleep 2s
Type "head -3 reimport.csv"
Enter
Wait
Sleep 3s

Type "clear"
Enter
# The forgotten-@ guard, then the real upload.
Type "dci ingest-datahub-events-csv provider: cli-demo-import, file: reimport.csv"
Sleep 800ms
Enter
Wait+Screen /@reimport/
Wait
Sleep 3s
Type "dci ingest-datahub-events-csv provider: cli-demo-import, file: @reimport.csv"
Sleep 800ms
Enter
Wait+Screen /ingested/
Wait
Sleep 3s
