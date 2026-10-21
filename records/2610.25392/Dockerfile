FROM crucible-base:py311
WORKDIR /work
RUN ["bash","-lc","rm -rf /work/repo && git clone https://github.com/TSTS13/Paloma.git /work/repo && cd /work/repo && git checkout def3dfc612bd6554be8f2ed7d5174848d6a37119"]
RUN ["bash","-lc","mkdir -p /work/dataset/ETT-small && cd /work/dataset/ETT-small && curl -fL --retry 3 -o ETTh1.csv https://raw.githubusercontent.com/zhouhaoyi/ETDataset/main/ETT-small/ETTh1.csv && python - <<'PY'\nimport pandas as pd\np='/work/dataset/ETT-small/ETTh1.csv'\ndf=pd.read_csv(p)\nprint('shape:', df.shape)\nprint('columns:', list(df.columns))\nprint('first date:', df['date'].iloc[0], 'last date:', df['date'].iloc[-1])\nPY\nsha256sum /work/dataset/ETT-small/ETTh1.csv"]
RUN ["bash","-lc","mkdir -p /work/paper && cd /work/paper && (curl -fL --retry 2 -o source.tar.gz https://export.arxiv.org/e-print/2610.25392 || true) && (curl -fL --retry 2 -o paper.pdf https://arxiv.org/pdf/2610.25392 || true) && ls -lh /work/paper && file /work/paper/* 2>/dev/null"]
