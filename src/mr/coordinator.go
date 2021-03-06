/*
 * @Description:
 * @User: Snaper <532990528@qq.com>
 * @Date: 2021-06-16 12:25:17
 * @LastEditTime: 2021-06-26 01:41:37
 */

package mr

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"sync"
	"time"
)

//程序master，协调器，负责分发委派任务
type Coordinator struct {
	taskType            int
	nFile               int             //要统计的文本数量
	completedMapTask    int             //完成的Map任务数量
	completedReduceTask int             //完成的reduce任务数量
	QMapTask            chan MapTask    //保存Map任务，即文件路径，因为存在并发所以使用chan保存
	QReduceTask         chan ReduceTask //保存reduce任务，即文件路径，因为存在并发所以使用chan保存
	MMapProccess        map[int]*TaskProccess
	MReduceProccess     map[int]*TaskProccess
	MapOutputFile       []string   //map任务输出的文件
	ReduceOutputFile    []string   //reduce任务输出的文件
	Lock                sync.Mutex //互斥锁，保证协调器在工作时的线程安全性
}

//map任务类
type MapTask struct {
	TaskSeqNum int    //任务序号
	Filename   string //任务路径
}

//reduce任务类
type ReduceTask struct {
	TaskSeqNum int //任务序号
	MTaskNum   int
	Filename   string //任务路径
}

//任务监控类
type TaskProccess struct {
	TaskType   int
	ExpireTime int64
	Done       chan bool
	MTask      MapTask
	RTask      ReduceTask
}

/**
 * @name: RedoWork
 * @desc:
 * @param {*MrRpcArgs} args
 * @param {*MrRpcReply} reply
 * @return {*}
 */
func (c *Coordinator) RedoWork(args *MrRpcArgs, reply *MrRpcReply) error {
	switch args.TaskType {
	case MAP_TASK:
		c.QMapTask <- MapTask{args.TaskSeqNum, args.FilePaths[0]}
	case REDUCE_TASK:
		c.QReduceTask <- ReduceTask{args.TaskSeqNum, c.nFile, ""}
	}
	log.Printf("word %v re-add success\n", args.TaskSeqNum)
	return nil
}

/**
 * @name: SendTask
 * @desc: 请求任务
 * @param {*MrRpcArgs} args
 * @param {*MrRpcReply} reply
 * @return {*}
 */

func (c *Coordinator) SendTask(args *MrRpcArgs, reply *MrRpcReply) error {
	c.Lock.Lock()
	curTaskType := c.taskType
	c.Lock.Unlock()
	reply.TaskType = curTaskType
	switch curTaskType {
	case MAP_TASK:
		if len(c.QMapTask) == 0 {
			reply.TaskType = TIME_WAIT
			return errors.New("MapTaskQueue is empty")
		}
		task := <-c.QMapTask
		reply.MTask = task
		c.Lock.Lock()
		c.MMapProccess[task.TaskSeqNum] = &TaskProccess{MAP_TASK, time.Now().Unix(), make(chan bool, 1), task, ReduceTask{}}
		go c.monitor(c.MMapProccess[task.TaskSeqNum], c.QMapTask, c.QReduceTask)
		c.Lock.Unlock()
	case REDUCE_TASK:
		if len(c.QReduceTask) == 0 {
			reply.TaskType = TIME_WAIT
			return errors.New("ReduceTaskQueue is empty")
		}
		task := <-c.QReduceTask
		reply.RTask = task
		c.Lock.Lock()
		c.MReduceProccess[task.TaskSeqNum] = &TaskProccess{REDUCE_TASK, time.Now().Unix(), make(chan bool, 1), MapTask{}, task}

		go c.monitor(c.MReduceProccess[task.TaskSeqNum], c.QMapTask, c.QReduceTask)
		c.Lock.Unlock()
	}

	return nil
}

/**
 * @name: CompleteTask
 * @desc: 任务完成，向协调器汇报
 * @param {*MrRpcArgs} args
 * @param {*MrRpcReply} reply
 * @return {*}
 */
func (c *Coordinator) CompleteTask(args *MrRpcArgs, reply *MrRpcReply) error {
	c.Lock.Lock()
	switch args.TaskType {
	case MAP_TASK:

		c.completedMapTask++
		c.MMapProccess[args.TaskSeqNum].Done <- true
		c.MapOutputFile = append(c.MapOutputFile, args.FilePaths...)
		if c.completedMapTask == c.nFile {
			c.taskType = REDUCE_TASK
			for i := 0; i < N_REDUCE; i++ {
				c.QReduceTask <- ReduceTask{i, c.nFile, ""}
			}
		}
	case REDUCE_TASK:
		c.completedReduceTask++
		c.MReduceProccess[args.TaskSeqNum].Done <- true
		c.ReduceOutputFile = append(c.ReduceOutputFile, args.FilePaths[0])
		if c.completedReduceTask == N_REDUCE {
			c.taskType = DONE

		}
	}
	c.Lock.Unlock()
	return nil
}

/**
 * @name:
 * @desc:
 * @param {TaskProccess} task
 * @param {chanMapTask} mapTaskQueue
 * @param {chanReduceTask} reduceTaskQueue
 * @return {*}
 */
func (c *Coordinator) monitor(task *TaskProccess, mapTaskQueue chan MapTask, reduceTaskQueue chan ReduceTask) {
	for {
	
	select {
	case done := <-task.Done:
		if done == true {
			return
		}
	default:
		curTime := time.Now().Unix()
		if curTime-task.ExpireTime >= MAX_TIME {
			switch task.TaskType {
			case MAP_TASK:
				mapTaskQueue <- task.MTask
				log.Printf("[INFO] MapTask %d TimeOut (maybe crash), redo file: %s", task.MTask.TaskSeqNum, task.MTask.Filename)
			case REDUCE_TASK:
				reduceTaskQueue <- task.RTask
				log.Printf("[INFO] ReduceTask %d TimeOut (maybe crash), redo file: %s", task.RTask.TaskSeqNum, task.RTask.Filename)
			}
			return
		}
	}
	}
}

//
// start a thread that listens for RPCs from worker.go
//
func (c *Coordinator) server() {
	rpc.Register(c)
	rpc.HandleHTTP()
	//l, e := net.Listen("tcp", ":1234")
	sockname := coordinatorSock()
	os.Remove(sockname)
	l, e := net.Listen("unix", sockname)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	go http.Serve(l, nil)
}

//
// main/mrcoordinator.go calls Done() periodically to find out
// if the entire job has finished.
//
func (c *Coordinator) Done() bool {
	ret := false
	c.Lock.Lock()
	if c.taskType == DONE {
		ret = true
	}
	c.Lock.Unlock()
	return ret
}

/**
 * @name: MakeCoordinator
 * @desc:
 * @param {[]string} files
 * @param {int} nReduce
 * @return {*}
 */
func MakeCoordinator(files []string, nReduce int) *Coordinator {
	c := Coordinator{}
	c.nFile = len(files)
	c.QMapTask = make(chan MapTask, c.nFile)
	c.QReduceTask = make(chan ReduceTask, N_REDUCE)
	c.taskType = MAP_TASK
	c.MapOutputFile = make([]string, c.nFile)
	c.ReduceOutputFile = make([]string, N_REDUCE)
	c.MMapProccess = make(map[int]*TaskProccess)
	c.MReduceProccess = make(map[int]*TaskProccess)
	for i, file := range files {
		c.QMapTask <- MapTask{i, file}
	}

	c.server()
	return &c
}
