# Solution

<!-- checkpoint: proxy-service-ready -->

Update `/etc/systemd/system/breakfix-proxy.service` on `proxy` so its upstream is `app:8080`, reload systemd, and restart the service.

<!-- checkpoint: application-reachable -->

From `client`, confirm that `curl http://proxy:8080` returns `breakfix-multi-node`.
