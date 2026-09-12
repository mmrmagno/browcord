FROM alpine:3.21
RUN apk add --no-cache iptables ip6tables bash
COPY deploy/netguard/apply.sh /usr/local/bin/apply
RUN chmod +x /usr/local/bin/apply
ENTRYPOINT ["/usr/local/bin/apply"]
