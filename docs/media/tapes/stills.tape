Source common.tape
Set FontSize 28
Set Width 2276
Set Height 1224
Source setup.tape
Type@2ms "testdash"
Enter
Wait+Screen@300s /tests ready/
Sleep 500ms

# The last run, before this one
Show
Sleep 800ms
Screenshot shots/history.png
Type "H"
Sleep 800ms
Screenshot shots/runs.png
Escape

# Picking: collapse the first classes, select a class and part of another
Down
Left
Down
Left
Down
Left
Down
Space
Down 9
Space
Space
Space
Type "g"
Sleep 1s
Screenshot shots/pick.png

# Filtering
Type "/"
Type "refund"
Sleep 800ms
Screenshot shots/filter.png
Escape
Escape
Sleep 300ms

# Running everything
Type "r"
Wait+Screen@90s /4 of [0-9]+ done/
Sleep 300ms
Screenshot shots/running.png
Wait+Screen@90s /FAILED [0-9]/
Sleep 6s
Screenshot shots/finished.png

# The run's parts at full size: everything in one place, the activity, the classes
Type "3"
Type "]"
Sleep 300ms
PageUp
Up 3
Sleep 800ms
Screenshot shots/live.png
Type "]"
Sleep 800ms
Screenshot shots/activity.png
Type "]"
Sleep 800ms
Screenshot shots/classes.png
Escape
Type "1"

# A failed test and what happened while it ran
Type "s"
Down 4
Sleep 500ms
Type "3"
Down 30
Up
Sleep 800ms
Screenshot shots/failure.png

# The request in WireMock, zoomed
Enter
Sleep 800ms
Type "z"
Sleep 800ms
Screenshot shots/wiremock.png
Escape

# Another failed test's events by trace
Type "1"
Up 2
Type "t"
Type "3"
Sleep 800ms
Screenshot shots/traces.png
Type "t"

# Kafka with a message's headers
Type "2"
Type "g"
Down 3
Sleep 800ms
Type "3"
Type "h"
Sleep 800ms
Screenshot shots/kafka.png

# The application's warnings and errors, then the Kafka client's chatter hidden as noise
Type "2"
Type "g"
Down
Sleep 800ms
Type "3"
Type "e"
Sleep 800ms
Screenshot shots/logs.png
Type "/"
Type "UNKNOWN_TOPIC"
Enter
Type "N"
Sleep 800ms
Screenshot shots/noise.png
Escape
Escape
Type "e"

# Postgres's queries, laid out, and Redis's commands
Type "2"
Down 5
Sleep 800ms
Type "3"
PageDown 30
Up 2
Sleep 800ms
Screenshot shots/sql.png
Type "2"
Down
Sleep 800ms
Type "3"
PageDown 30
Up 3
Sleep 800ms
Screenshot shots/redis.png

# Settings, commands and keys
Type "1"
Ctrl+S
Sleep 800ms
Screenshot shots/settings.png
Escape
Ctrl+P
Type "run"
Sleep 800ms
Screenshot shots/palette.png
Escape
Type "?"
Sleep 800ms
Screenshot shots/help.png
Escape
Type "q"
Sleep 600ms
Screenshot shots/quit.png
Type "y"
Sleep 500ms
