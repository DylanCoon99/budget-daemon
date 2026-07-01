DEPLOYMENT INSTRUCTIONS
=======================

== macOS (local development / testing) ==

1. Copy the binary:
   cp budget /usr/local/bin/budget

2. Create your .env:
   mkdir -p ~/.budget
   cp .env.example ~/.budget/.env
   # Edit ~/.budget/.env with your API keys

3. Install the launchd service:
   cp deploy/com.budget.daemon.plist ~/Library/LaunchAgents/
   launchctl load ~/Library/LaunchAgents/com.budget.daemon.plist

4. Check status:
   launchctl list | grep budget

5. View logs:
   tail -f /tmp/budget-daemon.out.log

6. Stop:
   launchctl unload ~/Library/LaunchAgents/com.budget.daemon.plist


== Raspberry Pi (production) ==

1. Cross-compile:
   GOOS=linux GOARCH=arm64 go build -o budget ./cmd/budget/

2. Copy to Pi:
   scp budget pi@<pi-ip>:/opt/budget/budget
   scp .env.example pi@<pi-ip>:/opt/budget/.env

3. On the Pi, create a service user:
   sudo useradd -r -s /usr/sbin/nologin budget
   sudo chown -R budget:budget /opt/budget
   sudo chmod 600 /opt/budget/.env

4. Install systemd service:
   sudo cp deploy/budget-daemon.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable budget-daemon
   sudo systemctl start budget-daemon

5. Check status:
   sudo systemctl status budget-daemon
   sudo journalctl -u budget-daemon -f

6. Update the binary:
   # On your Mac:
   GOOS=linux GOARCH=arm64 go build -o budget ./cmd/budget/
   scp budget pi@<pi-ip>:/opt/budget/budget
   ssh pi@<pi-ip> "sudo systemctl restart budget-daemon"
