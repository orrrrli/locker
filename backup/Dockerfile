# Nightly encrypted database backup. Same base as the postgres service, so pg_dump
# always matches the server's major version.
FROM postgres:17-alpine
RUN apk add --no-cache age rclone
COPY backup.sh /usr/local/bin/backup.sh
COPY crontab /etc/crontabs/root
# busybox crond passes the container env to its jobs, so no env file is written to disk.
CMD ["crond", "-f", "-l", "8"]
