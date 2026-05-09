#compdef stash

_stash() {
    local -a commands
    commands=(
        'add:Stash a URL, file, or stdin snippet'
        'search:Full-text search across all stashed items'
        'find:Interactive fuzzy finder (requires fzf)'
        'list:List stashed items'
        'show:Show details of a stashed item'
        'edit:Edit a stashed item'
        'delete:Delete a stashed item'
        'archive:Soft-delete an item (hide from default list)'
        'unarchive:Restore an archived item'
        'open:Open a stashed item in its default application'
        'copy:Copy a field of a stashed item to the clipboard'
        'log:Show recent capture activity (rules, captures, errors)'
        'tag-log:Show recent manual tag activity'
        'shell-init:Print shell function + keybind for stash find integration'
        'tag:Manage tags'
        'collection:Manage collections'
        'link:Create a link between two items'
        'unlink:Remove a link between two items'
        'import:Import items from external sources'
        'bulk:Bulk operations on multiple items'
        'stats:Show stash statistics'
        'rules:Inspect and apply capture rules'
        'check:Check stash for data hygiene issues'
        'dupes:Find duplicate items'
        'backup:Create a backup of database and files'
        'restore:Restore from a backup'
        'refresh:Re-fetch content for a URL item'
        'chrome-host:Chrome Native Messaging host'
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
            local -a search_commands
            search_commands=(
                'save:Save a search for later'
                'list:List saved searches'
                'run:Run a saved search'
                'rename:Rename a saved search'
                'delete:Delete a saved search'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '*--tag=[Filter by tag]:tag:' \
                '*--exclude-tag=[Exclude tag]:tag:' \
                '--untagged[Only items with no tags]' \
                '--collection=[Filter by collection]:collection:' \
                '--after=[Created after]:date:' \
                '--before=[Created before]:date:' \
                '--recent=[Within duration]:duration (e.g. 7d, 2w, 6h):' \
                '--regex=[RE2 pattern]:pattern:' \
                '--include-archived[Also show archived items]' \
                '--archived[Only archived items]' \
                '-l+[Max results]:limit:' \
                '--limit=[Max results]:limit:' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'search command' search_commands
                ;;
            subargs)
                case $words[1] in
                save)
                    _arguments \
                        '--type=[Filter by type]:type:(url snippet file image email)' \
                        '*--tag=[Filter by tag]:tag:' \
                        '*--exclude-tag=[Exclude tag]:tag:' \
                        '--untagged[Only items with no tags]' \
                        '--collection=[Filter by collection]:collection:' \
                        '--after=[Created after]:date:' \
                        '--before=[Created before]:date:' \
                        '--recent=[Within duration]:duration:' \
                        '--regex=[RE2 pattern]:pattern:' \
                        '--live[Save as a Smart Collection (auto-refreshes in stash-mac)]' \
                        '-l+[Max results]:limit:' \
                        '--limit=[Max results]:limit:' \
                        '1:name:' \
                        '2:query:'
                    ;;
                rename)
                    _arguments '1:old name:' '2:new name:'
                    ;;
                run|delete)
                    _arguments '1:name:'
                    ;;
                esac
                ;;
            esac
            ;;
        find)
            _arguments \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '*--tag=[Filter by tag]:tag:' \
                '*--exclude-tag=[Exclude tag]:tag:' \
                '--untagged[Only items with no tags]' \
                '--collection=[Filter by collection]:collection:' \
                '--after=[Created after]:date:' \
                '--before=[Created before]:date:' \
                '--recent=[Within duration]:duration (e.g. 7d, 2w, 6h):' \
                '--regex=[RE2 pattern]:pattern:' \
                '--include-archived[Also show archived items]' \
                '--archived[Only archived items]' \
                '-l+[Max results]:limit:' \
                '--limit=[Max results]:limit:' \
                '--action=[Action on Enter]:action:(open copy-url copy-id edit delete print-id print-json)'
            ;;
        list)
            _arguments \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '*--tag=[Filter by tag]:tag:' \
                '*--exclude-tag=[Exclude tag]:tag:' \
                '--untagged[Only items with no tags]' \
                '--collection=[Filter by collection]:collection:' \
                '--after=[Created after]:date:' \
                '--before=[Created before]:date:' \
                '--recent=[Within duration]:duration:' \
                '--regex=[RE2 pattern]:pattern:' \
                '--include-archived[Also show archived items]' \
                '--archived[Only archived items]' \
                '-l+[Max results]:limit:' \
                '--limit=[Max results]:limit:'
            ;;
        archive|unarchive)
            _arguments \
                '(- *)--help[Show help]' \
                '--dry-run[Print what would be (un)archived without writing]' \
                '*:item id:'
            ;;
        check)
            _arguments \
                '(- *)--help[Show help]' \
                '--urls[Check for broken URLs]' \
                '--files[Check for orphaned/missing files]' \
                '--dupes[Check for duplicate content hashes]' \
                '--stream[Emit NDJSON events progressively]' \
                '--id=[Limit URL recheck to one item id]:item id:'
            ;;
        copy)
            _arguments \
                '(- *)--help[Show help]' \
                '--field=[Field to copy]:field:(url id title content notes)' \
                '1:item id:'
            ;;
        log)
            _arguments \
                '(- *)--help[Show help]' \
                '--type=[Filter by event type]:type:(fire skip retro capture error)' \
                '--rule=[Filter to events involving the named rule]:name:' \
                '--since=[Only events newer than DURATION]:duration (e.g. 30m, 1h, 7d, 1w):' \
                '--limit=[Maximum events to show]:N:' \
                '-l+[Maximum events to show]:N:' \
                '(--tail -f)'{--tail,-f}'[Follow the log live]'
            ;;
        tag-log)
            _arguments \
                '(- *)--help[Show help]' \
                '(--action -a)'{--action,-a}'=[Filter by action]:action:(add remove)' \
                '(--tag -t)'{--tag,-t}'=[Filter to events involving the named tag]:name:' \
                '--since=[Only events newer than DURATION]:duration (e.g. 30m, 1h, 7d, 1w):' \
                '(--limit -l)'{--limit,-l}'=[Maximum events to show]:N:'
            ;;
        shell-init)
            _arguments \
                '(- *)--help[Show help]' \
                '--shell=[Target shell]:shell:(zsh bash)'
            ;;
        show|delete|open|refresh)
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
                '-u+[New URL (link items)]:url:' \
                '--url=[New URL (link items)]:url:' \
                '*--add-tag=[Add tag]:tag:' \
                '*--remove-tag=[Remove tag]:tag:' \
                '-c+[Collection]:collection:' \
                '--collection=[Collection]:collection:' \
                '1:id:'
            ;;
        import)
            local -a import_commands
            import_commands=(
                'bookmarks:Import Chrome/Firefox bookmarks HTML'
                'pocket:Import Pocket HTML export'
                'pinboard:Import Pinboard JSON export'
                'notion:Import Notion export (zip or directory)'
                'obsidian:Import Obsidian vault'
                'backfill:Fetch content for URL items missing text'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'import command' import_commands
                ;;
            subargs)
                case $words[1] in
                bookmarks|pocket)
                    _arguments \
                        '*-T+[Extra tag]:tag:' \
                        '*--tag=[Extra tag]:tag:' \
                        '-c+[Collection]:collection:' \
                        '--collection=[Collection]:collection:' \
                        '--dry-run[Preview without saving]' \
                        '1:file:_files -g "*.html"'
                    ;;
                pinboard)
                    _arguments \
                        '*-T+[Extra tag]:tag:' \
                        '*--tag=[Extra tag]:tag:' \
                        '-c+[Collection]:collection:' \
                        '--collection=[Collection]:collection:' \
                        '--dry-run[Preview without saving]' \
                        '1:file:_files -g "*.json"'
                    ;;
                notion)
                    _arguments \
                        '*-T+[Extra tag]:tag:' \
                        '*--tag=[Extra tag]:tag:' \
                        '-c+[Collection]:collection:' \
                        '--collection=[Collection]:collection:' \
                        '--dry-run[Preview without saving]' \
                        '1:path:_files'
                    ;;
                obsidian)
                    _arguments \
                        '*-T+[Extra tag]:tag:' \
                        '*--tag=[Extra tag]:tag:' \
                        '-c+[Collection]:collection:' \
                        '--collection=[Collection]:collection:' \
                        '--dry-run[Preview without saving]' \
                        '1:vault path:_files -/'
                    ;;
                backfill)
                    _arguments \
                        '-l+[Max items]:limit:' \
                        '--limit=[Max items]:limit:'
                    ;;
                esac
                ;;
            esac
            ;;
        bulk)
            local -a bulk_commands
            bulk_commands=(
                'tag:Add or remove tags on multiple items'
                'delete:Delete multiple items'
                'collect:Add or remove items from a collection'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'bulk command' bulk_commands
                ;;
            subargs)
                case $words[1] in
                tag)
                    _arguments \
                        '*--add-tag=[Tag to add]:tag:' \
                        '*--remove-tag=[Tag to remove]:tag:' \
                        '--query=[Search query]:query:' \
                        '--type=[Filter by type]:type:(url snippet file image email)' \
                        '*--tag=[Filter by tag]:tag:' \
                        '--in-collection=[Filter by collection]:collection:' \
                        '--after=[Created after]:date:' \
                        '--before=[Created before]:date:' \
                        '-l+[Max items]:limit:' \
                        '--limit=[Max items]:limit:' \
                        '*:id:'
                    ;;
                delete)
                    _arguments \
                        '(-y --yes)'{-y,--yes}'[Skip confirmation]' \
                        '--query=[Search query]:query:' \
                        '--type=[Filter by type]:type:(url snippet file image email)' \
                        '*--tag=[Filter by tag]:tag:' \
                        '--in-collection=[Filter by collection]:collection:' \
                        '--after=[Created after]:date:' \
                        '--before=[Created before]:date:' \
                        '-l+[Max items]:limit:' \
                        '--limit=[Max items]:limit:' \
                        '*:id:'
                    ;;
                collect)
                    _arguments \
                        '-c+[Target collection]:collection:' \
                        '--collection=[Target collection]:collection:' \
                        '--remove[Remove from collection]' \
                        '--query=[Search query]:query:' \
                        '--type=[Filter by type]:type:(url snippet file image email)' \
                        '*--tag=[Filter by tag]:tag:' \
                        '--in-collection=[Filter by collection]:collection:' \
                        '--after=[Created after]:date:' \
                        '--before=[Created before]:date:' \
                        '-l+[Max items]:limit:' \
                        '--limit=[Max items]:limit:' \
                        '*:id:'
                    ;;
                esac
                ;;
            esac
            ;;
        stats)
            _arguments '(- *)--help[Show help]'
            ;;
        check)
            _arguments \
                '(- *)--help[Show help]' \
                '--urls[Check for broken URLs]' \
                '--files[Check for orphaned/missing files]' \
                '--dupes[Check for duplicate content]' \
                '--stream[Emit newline-delimited JSON events as findings arrive]'
            ;;
        rules)
            local -a rules_commands
            rules_commands=(
                'list:List configured rules'
                'test:Show which rules would apply to an item'
                'apply:Retroactively apply rules to existing items'
                'enable:Enable a rule by name'
                'disable:Disable a rule by name'
                'save:Upsert a rule from JSON on stdin'
                'rename:Rename a rule (updates rules.yaml and rewrites rules.log)'
                'remove:Delete a rule by name'
                'log:Show recent rule activity'
            )
            _arguments -C '1: :->cmd' '*:: :->args'
            case $state in
                cmd) _describe 'rules command' rules_commands ;;
                args)
                    case $words[1] in
                        apply)
                            _arguments \
                                '--dry-run[Preview changes without writing]' \
                                '--type[Limit to one item type]:type:(url snippet file image email)' \
                                '*--tag[Limit to items with these tags]:tag:' \
                                '--rule[Apply only the named rule]:name:'
                            ;;
                        log)
                            _arguments \
                                '--type[Filter by event type]:type:(fire skip retro capture error)' \
                                '--rule[Filter to events involving the named rule]:name:' \
                                '--limit[Maximum events to show]:N:' \
                                '-l[Maximum events to show]:N:' \
                                '--since[Only events newer than DURATION]:duration:' \
                                '--tail[Stream new events as they arrive]' \
                                '-f[Stream new events as they arrive]'
                            ;;
                        rename)
                            _arguments '1:old name:' '2:new name:'
                            ;;
                        enable|disable|remove)
                            _arguments '1:rule name:'
                            ;;
                    esac
                    ;;
            esac
            ;;
        dupes)
            local -a dupes_commands
            dupes_commands=(
                'dismiss:Dismiss a duplicate pair'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '--type=[Filter by type]:type:(url snippet file image email)' \
                '--threshold=[Title similarity threshold]:threshold:' \
                '--include-dismissed[Include previously dismissed pairs]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'dupes command' dupes_commands
                ;;
            subargs)
                case $words[1] in
                dismiss)
                    _arguments '1:id1:' '2:id2:'
                    ;;
                esac
                ;;
            esac
            ;;
        backup)
            _arguments \
                '(- *)--help[Show help]' \
                '--list[List available backups]' \
                '--db-only[Database only, skip files]'
            ;;
        restore)
            _arguments \
                '(- *)--help[Show help]' \
                '1:backup file:_files -g "*.db"'
            ;;
        chrome-host)
            local -a chrome_commands
            chrome_commands=(
                'install:Register native messaging host with Chrome'
                'uninstall:Remove native messaging host manifest'
            )
            _arguments -C \
                '(- *)--help[Show help]' \
                '1: :->subcmd' \
                '*:: :->subargs'
            case $state in
            subcmd)
                _describe 'chrome-host command' chrome_commands
                ;;
            esac
            ;;
        tag)
            local -a tag_commands
            tag_commands=(
                'list:List all tags'
                'rename:Rename a tag'
                'graph:Show tag co-occurrence graph'
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
