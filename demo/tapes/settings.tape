# Shared settings for every demo section. Sourced first by each section
# tape; a section may add its own Set lines after this Source and before
# it Sources setup.tape (VHS ignores Set directives that follow a command).
# 4K master: 3840x2160 with Menlo 36px is the same 153x41 cell layout as
# 1920x1080 with Menlo 18px, so scenes compose identically at either size.
Set Shell zsh
Set FontFamily "Menlo"
Set FontSize 36
Set Width 3840
Set Height 2160
Set Padding 80
Set Theme "Catppuccin Mocha"
# VHS assembles the frames it managed to capture at this rate, so a rate the
# 4K page cannot sustain compresses time (measured: ~20 fps captured while
# assembling at the default 50 made a 13 s scene play in 5 s). 15 keeps the
# recording at wall-clock speed.
Set Framerate 15
Set TypingSpeed 60ms
Set WaitTimeout 180s
Set WaitPattern /❯$/
