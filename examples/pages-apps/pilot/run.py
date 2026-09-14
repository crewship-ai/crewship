import argparse, json, os, pathlib, secrets, subprocess, tempfile, time
parser=argparse.ArgumentParser(description="Run isolated real MySQL/Ansible collectors; never contacts a live Crewship server")
parser.add_argument('--mysql-image',required=True,help='Locally installed pinned mysql image (name@sha256:...)')
parser.add_argument('--collector-image',required=True,help='Locally built pinned collector image (name@sha256:...)')
parser.add_argument('--output',required=True,help='Destination for bounded, credential-free verdicts')
args=parser.parse_args()
for image in (args.mysql_image,args.collector_image):
    if '@sha256:' not in image or len(image.rsplit('@sha256:',1)[1])!=64:
        parser.error('Images must use a pinned name@sha256 digest')
work=pathlib.Path(tempfile.mkdtemp(prefix='crewship-pages-pilot-'))
os.chmod(work,0o700)
network=work.name
mysql=work.name+'-mysql'
collector=str(pathlib.Path(__file__).resolve().parent.parent/'scripts'/'collect_status.py')
def run(args,**kw):
    return subprocess.run(args,check=True,capture_output=True,text=True,timeout=kw.pop('timeout',60),**kw)
password=secrets.token_hex(24)
(work/'mysql.env').write_text('MYSQL_ROOT_PASSWORD='+password+'\nMYSQL_ROOT_HOST=%\n')
(work/'mysql.cnf').write_text('[client]\nhost=mysql\nuser=root\npassword='+password+'\n')
(work/'local.cnf').write_text('[client]\nuser=root\npassword='+password+'\n')
for file in ('mysql.env','mysql.cnf','local.cnf'):os.chmod(work/file,0o600)
(work/'inventory').write_text('localhost ansible_connection=local\n')
(work/'good.yaml').write_text('- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.assert:\n        that: true\n')
(work/'bad.yaml').write_text('- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.fail:\n        msg: intentionally failing pilot\n')
results={}
try:
    run(['docker','network','create','--internal',network])
    run(['docker','run','--pull=never','-d','--name',mysql,'--network',network,'--network-alias','mysql','--cpus=1','--memory=512m','--pids-limit=192','--tmpfs','/var/lib/mysql:rw,size=512m','--env-file',str(work/'mysql.env'),'-v',str(work/'local.cnf')+':/run/client.cnf:ro',args.mysql_image,'--innodb-redo-log-capacity=64M','--innodb-buffer-pool-size=64M','--performance-schema=OFF','--mysqlx=0'])
    for i in range(50):
        probe=subprocess.run(['docker','exec',mysql,'mysql','--defaults-extra-file=/run/client.cnf','--protocol=tcp','--host=127.0.0.1','--batch','--execute=SELECT 1'],capture_output=True,timeout=5)
        if probe.returncode==0:break
        time.sleep(1)
    else:raise RuntimeError('MySQL fixture did not become ready')
    def collect(kind,playbook='good.yaml'):
        cmd=['docker','run','--pull=never','--rm','--network',network,'--cpus=1','--memory=256m','--pids-limit=96','--cap-drop','ALL','--security-opt','no-new-privileges','--read-only','--tmpfs','/tmp:rw,size=32m','-e','HOME=/tmp','-e','ANSIBLE_LOCAL_TEMP=/tmp/ansible','-e','MYSQL_DEFAULTS_FILE=/work/mysql.cnf','-e','ANSIBLE_INVENTORY_FILE=/work/inventory','-e','ANSIBLE_PLAYBOOK_FILE=/work/'+playbook,'-v',str(work)+':/work:ro','-v',collector+':/collect.py:ro',args.collector_image,'python3','/collect.py',kind,'--routine-output']
        result=json.loads(run(cmd,timeout=140).stdout)
        assert password not in json.dumps(result)
        return result
    results['mysql_healthy']=collect('mysql')
    assert results['mysql_healthy']['producer_state']=='ok',results['mysql_healthy']
    run(['docker','stop','-t','5',mysql])
    results['mysql_unavailable']=collect('mysql')
    assert results['mysql_unavailable']['producer_state']=='failed'
    results['ansible_check']=collect('ansible')
    assert results['ansible_check']['producer_state']=='ok',results['ansible_check']
    results['ansible_failed']=collect('ansible','bad.yaml')
    assert results['ansible_failed']['producer_state']=='failed'
    pathlib.Path(args.output).write_text(json.dumps(results,indent=2)+'\n')
    print(json.dumps(results,indent=2))
finally:
    subprocess.run(['docker','rm','-f',mysql],capture_output=True)
    subprocess.run(['docker','network','rm',network],capture_output=True)
    import shutil
    shutil.rmtree(work)
