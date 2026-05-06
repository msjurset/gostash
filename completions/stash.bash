_stash() {
    local cur prev words cword
    _init_completion || return

    local commands="add search list show edit delete open link unlink import bulk stats check dupes backup restore refresh chrome-host rules tag collection ui man help"
    local global_flags="--help --version --json --db"

    if [[ $cword -eq 1 ]]; then
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "$global_flags" -- "$cur"))
        else
            COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        fi
        return
    fi

    case "${words[1]}" in
    add)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--title -t --tag -T --note -n --collection -c --type --delete -d --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
        else
            COMPREPLY=($(compgen -f -- "$cur"))
        fi
        ;;
    search)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "save list run delete" -- "$cur"))
        elif [[ "${words[2]}" == "save" ]]; then
            if [[ "$cur" == -* ]]; then
                COMPREPLY=($(compgen -W "--type --tag --collection --after --before --limit -l --help" -- "$cur"))
            fi
        elif [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --tag --collection --after --before --limit -l --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
        fi
        ;;
    list)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --tag --collection --after --before --limit -l --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
        fi
        ;;
    edit)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--title -t --note -n --extracted-text -e --add-tag --remove-tag --collection -c --help" -- "$cur"))
        fi
        ;;
    delete)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--yes -y --help" -- "$cur"))
        fi
        ;;
    link)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--label -l --directed --help" -- "$cur"))
        fi
        ;;
    unlink)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--help" -- "$cur"))
        fi
        ;;
    import)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "bookmarks pocket pinboard notion obsidian backfill" -- "$cur"))
        else
            local subcmd="${words[2]}"
            case "$subcmd" in
            bookmarks|pocket)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--tag -T --collection -c --dry-run --help" -- "$cur"))
                else
                    COMPREPLY=($(compgen -f -- "$cur"))
                fi
                ;;
            pinboard)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--tag -T --collection -c --dry-run --help" -- "$cur"))
                else
                    COMPREPLY=($(compgen -f -- "$cur"))
                fi
                ;;
            notion|obsidian)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--tag -T --collection -c --dry-run --help" -- "$cur"))
                else
                    COMPREPLY=($(compgen -f -- "$cur"))
                fi
                ;;
            backfill)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--limit -l --help" -- "$cur"))
                fi
                ;;
            esac
        fi
        ;;
    bulk)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "tag delete collect" -- "$cur"))
        else
            local subcmd="${words[2]}"
            case "$subcmd" in
            tag)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--add-tag --remove-tag --query --type --tag --in-collection --after --before --limit -l --help" -- "$cur"))
                elif [[ "$prev" == "--type" ]]; then
                    COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
                fi
                ;;
            delete)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--yes -y --query --type --tag --in-collection --after --before --limit -l --help" -- "$cur"))
                elif [[ "$prev" == "--type" ]]; then
                    COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
                fi
                ;;
            collect)
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "--collection -c --remove --query --type --tag --in-collection --after --before --limit -l --help" -- "$cur"))
                elif [[ "$prev" == "--type" ]]; then
                    COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
                fi
                ;;
            esac
        fi
        ;;
    stats)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--help" -- "$cur"))
        fi
        ;;
    check)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--urls --files --dupes --stream --help" -- "$cur"))
        fi
        ;;
    rules)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "list test apply enable disable save remove log" -- "$cur"))
        elif [[ "${COMP_WORDS[2]}" == "apply" && "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--dry-run --type --tag --rule --help" -- "$cur"))
        elif [[ "${COMP_WORDS[2]}" == "log" && "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --rule --limit --since --tail -f -l --help" -- "$cur"))
        fi
        ;;
    dupes)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "dismiss" -- "$cur"))
        elif [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --threshold --include-dismissed --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "url snippet file image email" -- "$cur"))
        fi
        ;;
    backup)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--list --db-only --help" -- "$cur"))
        fi
        ;;
    restore)
        if [[ "$cur" != -* ]]; then
            COMPREPLY=($(compgen -f -- "$cur"))
        fi
        ;;
    chrome-host)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "install uninstall" -- "$cur"))
        fi
        ;;
    tag)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "list rename graph" -- "$cur"))
        fi
        ;;
    collection)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "list create delete show" -- "$cur"))
        elif [[ "${words[2]}" == "create" && "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--description -d --help" -- "$cur"))
        fi
        ;;
    esac
}

complete -F _stash stash
