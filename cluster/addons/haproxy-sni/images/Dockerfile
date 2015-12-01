FROM ubuntu:14.04

MAINTAINER Bracket Computing Inc.

# install supervisord
RUN apt-get update && apt-get install -y supervisor ca-certificates file
RUN mkdir -p /var/log/supervisor
RUN mkdir -p /var/run

# Install Haproxy.
RUN \
  sed -i 's/^# \(.*-backports\s\)/\1/g' /etc/apt/sources.list && \
  apt-get update && \
  apt-get install -y haproxy -t trusty-backports && \
  sed -i 's/^ENABLED=.*/ENABLED=1/' /etc/default/haproxy && \
  rm -rf /var/lib/apt/lists/*

COPY supervisord.conf /etc/supervisor/conf.d/supervisord.conf

# create pid file
RUN touch /haproxy.pid /haproxy.cfg

# Define working directory.
WORKDIR /etc/haproxy

# Add monitor binary
COPY haproxy-monitor /bin/

# Expose ports.
EXPOSE 80 443

# Define default command.
CMD ["/usr/bin/supervisord"]
