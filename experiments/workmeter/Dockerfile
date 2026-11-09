ARG RUNTIME_IMAGE
FROM ${RUNTIME_IMAGE}
COPY launch server.py runner.py protocol.py validate.py preflight.py runtime-manifest.json /opt/interpreter-meter/
COPY fixtures/ /opt/interpreter-meter/fixtures/
COPY pypy/ /opt/interpreter-meter/pypy/
RUN python3 -c 'from pathlib import Path; p=Path("/opt/interpreter-meter/fixtures"); [x.chmod(0o555 if x.is_dir() else 0o444) for x in [p,*p.rglob("*")]]'
ENTRYPOINT ["/usr/bin/tini","--"]
CMD ["/usr/bin/python3","-B","/opt/interpreter-meter/server.py"]
