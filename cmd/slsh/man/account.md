Prints this avatar's own account details: the email address the grid
has on file and whether the directory shows the avatar.

    account

For example:

    /$ account
    email       somebody@example.invalid
    directory   default

The address above is made up.

## Private

These are the account's own, so slsh asks only when you type this.  It
does not read them at login, no other command prints them, and nothing
writes them to a log.  Ask in a terminal you would not mind being
looked over in, and take care where the output is pasted.

## What it asks

The grid is asked as the viewer asks: the region's `UserInfo`
capability when it offers one, and the older `UserInfoRequest` message
when it does not.  A refusal is reported with the grid's own words.
It gives up after five seconds.

## The IM-to-email flag

The grid also sends a flag for forwarding instant messages to email.
Second Life has retired the feature, and the viewer ignores the flag
there, so this does not print it.
