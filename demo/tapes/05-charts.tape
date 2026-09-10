Output out/05-charts.mp4
Source tapes/settings.tape
Source tapes/setup.tape

Type 'dci get-report "CLI Demo - Top 10 Services (6 months)" --chart'
Sleep 800ms
Enter
Wait+Screen /cost by period/
Wait
Sleep 5s

Type "clear"
Enter
Type 'dci get-report "CLI Demo - Top 10 Services (6 months)" --chart=treemap'
Sleep 800ms
Enter
Wait+Screen /cost by group/
Wait
Sleep 5s

Type "clear"
Enter
Type 'dci get-report "CLI Demo - Top 10 Services (6 months)" --chart=heatmap'
Sleep 800ms
Enter
Wait+Screen /cost by period/
Wait
Sleep 3s
