FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

RUN pip install --no-cache-dir https://github.com/explosion/spacy-models/releases/download/en_core_web_sm-3.7.1/en_core_web_sm-3.7.1-py3-none-any.whl

COPY spacy_extract.py .

EXPOSE 8001

CMD ["uvicorn", "spacy_extract:app", "--host", "0.0.0.0", "--port", "8001"]
