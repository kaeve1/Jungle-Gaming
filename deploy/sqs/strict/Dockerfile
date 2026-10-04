FROM ministackorg/ministack:1.5.20@sha256:051edcb3daed7ca3bdfe06c3c5e1427473ba656e8211f8c1d342543b277e982d
ENV PYTHONPATH=/opt/ministack:/strict
COPY strict_auth.py run.py durable_state.py test_strict_auth.py test_durable_state.py /strict/
RUN cd /strict && python -m unittest -v test_strict_auth test_durable_state
ENTRYPOINT ["python", "/strict/run.py"]
