_stash() {
    local cur prev words cword
    _init_completion || return

    local commands="add search list show edit delete open tag collection ui man help"
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
            COMPREPLY=($(compgen -W "--title -t --tag -T --note -n --collection -c --type --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "link snippet file image email" -- "$cur"))
        else
            COMPREPLY=($(compgen -f -- "$cur"))
        fi
        ;;
    search)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --tag --collection --after --before --limit -l --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "link snippet file image email" -- "$cur"))
        fi
        ;;
    list)
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "--type --tag --collection --after --before --limit -l --help" -- "$cur"))
        elif [[ "$prev" == "--type" ]]; then
            COMPREPLY=($(compgen -W "link snippet file image email" -- "$cur"))
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
    tag)
        if [[ $cword -eq 2 ]]; then
            COMPREPLY=($(compgen -W "list rename" -- "$cur"))
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
