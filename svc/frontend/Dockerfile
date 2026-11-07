FROM python:3.12-slim

RUN pip install --no-cache-dir httpbin gunicorn

EXPOSE 5173
CMD ["gunicorn", "--bind", "0.0.0.0:5173", "httpbin:app"]
