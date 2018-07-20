FROM python:2

RUN apt-get update
RUN apt-get install -y rsync

RUN pip install j2cli

ADD rsyncd.conf /etc/rsyncd.conf
ADD entrypoint.sh /entrypoint.sh

ENTRYPOINT [ "/entrypoint.sh" ]
