# Import and export

## In the app

Account, Export saves an Evado JSON file containing lists, todos, notes and
checklists, including completed todos. Deleted todos, assignees, list members
and account records are left out. Account, Import previews an Evado file
before asking you to bring it in. Existing todos are always skipped, including
deleted ones, and sharing is not copied. Past reminders and reminders on
completed todos are left off.

## From the command line

The CLI uses the same file for a server account or, with `-local`, the todos
on this computer:

```sh
evado export todos.json
evado import -dry-run todos.json
evado import todos.json
evado -local export > todos.json
evado -local import todos.json
```

`export` without a path writes JSON to standard output; named files use `0600`
permissions on Unix. `import -` reads standard input. `import -list <id>` puts
the todos into one writable list. `-json` prints the import report for
scripts. A server import that stops at its request limit prints its report and
exits with code 10; run it again with the same file to continue. Neither the
CLI nor the TUI retries a partial import automatically.

## The file

Files use the `evado-export` version 1 header. Older Evado exports and full
account exports can also be imported; unknown fields are ignored, and an
unsupported format or version is refused. Foreign todo-app files are imported
through the web app. The full copy of account records remains available there.

The next save upgrades the local todos file to version 2, which keeps import
identities across sync and deletion. This version reads older files; older
binaries refuse the upgraded file rather than lose that information.
