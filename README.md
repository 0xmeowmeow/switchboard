# switchboard

A launcher for the commands you forget you have.

Not a fuzzy finder. A **browsable list with descriptions**, so you don't have to
remember what exists — you look.

## Build

```bash
./build.sh
```

Installs to `~/.local/bin/sb`. Run with `sb`.

## Use

| key | |
|---|---|
| type | filter by name, description, group or note |
| ↑ ↓ | move |
| enter | run it |
| ctrl+a | add a command |
| ctrl+d | delete the selected one |
| ctrl+e | open the config in your editor |
| esc | quit |

The selected command's full shell line shows in the box at the bottom, along
with any note — so `cmus` can carry its keybindings, and you'll see them every
time you look at it.

## Config

`~/.config/switchboard/commands.conf`, created on first run.

```
group | name | description | command | optional note
```

Edit it directly or use the TUI. Sorted by group, then name.

## Why

Aliases are invisible. You have to already know the name to use one. This
inverts that: everything is listed, described, and grouped, so the tool tells
*you* what you have.
