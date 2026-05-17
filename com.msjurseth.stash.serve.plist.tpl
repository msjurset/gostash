<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<!-- launchd label — must match the .plist filename. -->
	<key>Label</key>
	<string>com.msjurseth.stash.serve</string>

	<!-- Bind to all interfaces on :9999 so the Android app on Wi-Fi /
	     WireGuard / Tailscale can hit it. Trust model is "anyone on
	     my Mac account / LAN" — pairing tokens gate API access.
	     --no-qr keeps the daemon log clean; the Mac app surfaces the
	     pairing QR in Settings → Phone pairing instead. -->
	<key>ProgramArguments</key>
	<array>
		<string>__BINARY_PATH__</string>
		<string>serve</string>
		<string>--no-qr</string>
	</array>

	<!-- Inherit a sensible PATH so spawning subprocesses (exiftool
	     etc.) works the same as from an interactive terminal. -->
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>

	<!-- Start on login + restart automatically when the process exits
	     (deploy unload/load cycle, crash, OS lifecycle event). -->
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>

	<!-- Don't restart faster than every 5s — protects against a tight
	     crash loop filling the log. -->
	<key>ThrottleInterval</key>
	<integer>5</integer>

	<!-- Log to ~/Library/Logs so Console.app surfaces it under the
	     user and `log show` finds it without root. -->
	<key>StandardOutPath</key>
	<string>__LOG_PATH__</string>
	<key>StandardErrorPath</key>
	<string>__LOG_PATH__</string>

	<!-- Background QoS — HTTP serving doesn't need foreground priority,
	     and keeps stash serve out of CPU contention with whatever's
	     in focus. -->
	<key>ProcessType</key>
	<string>Background</string>
</dict>
</plist>
