# Nightly encrypted database backup. Same base as the postgres service, so pg_dump
# always matches the server's major version. Built in CI, so it pulls from mirror.gcr.io
# (anonymous ECR Public pulls hit a per-IP data limit on shared runners, OPS-CI-1).
FROM mirror.gcr.io/library/postgres:17-alpine
RUN apk add --no-cache age rclone
COPY backup.sh /usr/local/bin/backup.sh
COPY crontab /etc/crontabs/root
# busybox crond passes the container env to its jobs, so no env file is written to disk.
CMD ["crond", "-f", "-l", "8"]
