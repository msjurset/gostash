#compdef stash

_stash() {
    local -a commands
    commands=(
        'add:Stash a URL, file, or stdin snippet'
        'search:Full-text search across all stashed items'
        'list:List stashed items'
        'show:Show details of a stashed item'
        'edit:Edit a stashed item'
        'delete:Delete a stashed item'
        'open:Open a stashed item in its default application'
        'tag:Manage tags'
        'collection:Manage collections'
        'link:Create a link between two items'
        'unlink:Remove a link between two items'
        'ui:Interactive TUI for browsing and searching'
        'man:Display the stash manual page'
        'help:Help about any command'
    )

    _arguments -C \
        '(- *)--help[Show help]' \
        '(- *)--version[Show version]' \
        '--json[Output as JSON]' \
        '--db[Database path]:path:_files' \
        '1: :->cmd' \
        '*:: :->args'

    case $state in
    cmd)
        _describe 'command' commands
        ;;
    args)
        case $words[1] in
        add)
            _arguments \
                '(- *)--help[Show help]' \
                '-t+[Title]:title:' \
                '--title=[Title]:title:' \
                '*-T+[Tag]:tag:' \
                '*--tag=[Tag]:tag:' \
                '-n+[Note]:note:' \
                '--note=[Note]:note:' \
                '-c+[Collection]:collection:' \
                '--collection=[Collection]:collection:' \
                '--type=[Force type]:type:(url snippet file image email)' \
                '(-d --delete)'{-d,--delete}'[Delete source after stash]' \
                '1:source:_files'
            ;;
        search)
            _arguments \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '*--tag=[Filter by tag]:tag:' \
                '--collection=[Filter by collection]:collection:' \
                '--after=[Created after]:date:' \
                '--before=[Created before]:date:' \
                '-l+[Max results]:limit:' \
                '--limit=[Max results]:limit:' \
                '1:query:'
            ;;
        list)
            _arguments \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '*--tag=[Filter by tag]:tag:' \
                '--collection=[Filter by collection]:collection:' \
                '--after=[Created after]:date:' \
                '--before=[Created before]:date:' \
                '-l+[Max results]:limit:' \
                '--limit=[Max results]:limit:'
            ;;
        show|delete|open)
            _arguments \
                '(- *)--help[Show help]' \
                '1:id:'
            ;;
        link)
            _arguments \
                '(- *)--help[Show help]' \
                '-l+[Label]:label:' \
                '--label=[Label]:label:' \
                '--directed[Create directed link]' \
                '1:id1:' \
                '2:id2:'
            ;;
        unlink)
            _arguments \
                '(- *)--help[Show help]' \
                '1:id1:' \
                '2:id2:'
            ;;
        edit)
            _arguments \
                '(- *)--help[Show help]' \
                '-t+[Title]:title:' \
                '--title=[Title]:title:' \
                '-n+[Note]:note:' \
                '--note=[Note]:note:' \
                '-e+[Extracted text]:text:' \
                '--extracted-text=[Extracted text]:text:' \
                '*--add-tag=[Add tag]:tag:' \
                '*--remove-tag=[Remove tag]:tag:' \
                '-c+[Collection]:collection:' \
                '--collection=[Collection]:collection:' \
                '1:id:'
            ;;
        tag)
            local -a tag_commands
            tag_commands=(
                'list:List all tags'
                'rename:Rename a tag'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'tag command' tag_commands
                ;;
            subargs)
                case $words[1] in
                rename)
                    _arguments '1:old name:' '2:new name:'
                    ;;
                esac
                ;;
            esac
            ;;
        collection)
            local -a col_commands
            col_commands=(
                'list:List all collections'
                'create:Create a new collection'
                'delete:Delete a collection'
                'show:Show items in a collection'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'collection command' col_commands
                ;;
            subargs)
                case $words[1] in
                create)
                    _arguments \
                        '-d+[Description]:description:' \
                        '--description=[Description]:description:' \
                        '1:name:'
                    ;;
                delete|show)
                    _arguments '1:name:'
                    ;;
                esac
                ;;
            esac
            ;;
        esac
        ;;
    esac
}

_stash "$@"
