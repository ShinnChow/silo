from pathlib import Path
import os,subprocess,socket,tempfile,time,urllib.request,urllib.error,signal,json
root=Path('/Users/vonng/tmp/silo-r8-01a0a5b9/capacity-probe');root.mkdir(exist_ok=True)
with socket.socket() as reservation:
 reservation.bind(('127.0.0.1',0));port=reservation.getsockname()[1]
env={k:v for k,v in os.environ.items() if not k.startswith(('MINIO_','MC_','SILO_'))}
env.update(MINIO_ROOT_USER='silo',MINIO_ROOT_PASSWORD='silo1234',MINIO_BROWSER='off',MINIO_CI_CD='1',MC_HOST_silo=f'http://silo:silo1234@127.0.0.1:{port}/')
url=f'http://127.0.0.1:{port}'
with (root/'server.log').open('w') as log:
 proc=subprocess.Popen(['/Users/vonng/tmp/silo-r8-01a0a5b9/silo-v2','server',f'--address=127.0.0.1:{port}','--read-header-timeout=5s','--idle-timeout=5s',str(root/'data')],env=env,stdout=log,stderr=subprocess.STDOUT)
 try:
  for _ in range(100):
   try:
    with urllib.request.urlopen(url+'/minio/health/live',timeout=1):break
   except Exception:time.sleep(.1)
  cli=['/opt/homebrew/bin/mcli','--config-dir',str(root/'mcli')]
  for args in [['mb','silo/testbucket'],['anonymous','set','public','silo/testbucket']]:
   r=subprocess.run(cli+args,env=env,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT);assert r.returncode==0,r.stdout
  req=urllib.request.Request(url+'/testbucket/testobject',data=b'x'*30,method='PUT')
  try:
   with urllib.request.urlopen(req,timeout=10) as response:result={'status':response.status,'body':response.read().decode()}
  except urllib.error.HTTPError as error:result={'status':error.code,'body':error.read().decode()}
  print(json.dumps(result,indent=2))
 finally:
  proc.send_signal(signal.SIGTERM)
  try:proc.wait(timeout=10)
  except subprocess.TimeoutExpired:proc.kill();proc.wait()
